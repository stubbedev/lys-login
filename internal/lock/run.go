package lock

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-shell/config"
	"github.com/stubbedev/lys-shell/imagedecode"
	"github.com/stubbedev/lys-shell/logging"
	"github.com/stubbedev/lys-shell/outputsize"
	"github.com/stubbedev/lys-shell/service/logind"
	"github.com/stubbedev/lys-shell/shell/apptheme"
	"github.com/stubbedev/lys-shell/shell/lockclient"
	"github.com/stubbedev/lys-shell/useraccount"
	"github.com/stubbedev/lys-ui/app"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-login/internal/auth"
	loginscreen "github.com/stubbedev/lys-login/internal/screen"
)

// Errors Run ends with, which `lys-login lock` maps to lockclient's exit
// codes.
var (
	ErrHeld    = errors.New("lock: another lys-login holds the session locked")
	ErrRefused = errors.New("lock: the session was not locked")
)

// Run is `lys-login lock`: it locks the session with the login screen in
// its lock mode and returns once it unlocks, by password or by logind's
// Unlock. It writes lockclient.LockedLine to stdout when the compositor confirms
// the lock. One lys-login per session holds the lock: a second Run
// returns ErrHeld.
func Run(cfg *config.Config, stdout io.Writer, vaultHelper []string) error {
	vu := newVaultUnlock(cfg.Bitwarden.UnlockWithSession, vaultHelper)
	defer vu.wait()
	verified := auth.NewVerified(vu.start)
	defer verified.Wait()
	release, err := holdSession(xdg.OS)
	if err != nil {
		return err
	}
	defer release()
	app.SetLogger(logging.Library())
	sess, err := app.Connect()
	if err != nil {
		return fmt.Errorf("lock: connect: %w", err)
	}
	defer sess.Close()
	a := app.NewApplication(sess)
	defer apptheme.Setup(a)()
	base, err := app.Font(cfg.General.FontSans, 16)
	if err != nil {
		return fmt.Errorf("lock: font %q: %w", cfg.General.FontSans, err)
	}
	wall, err := loginscreen.NewSystemClock(a.Invoke)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer func() {
		if err := wall.Close(); err != nil {
			log.Printf("lock: close the clock: %v", err)
		}
	}()
	lockedLine := newLatest("the locked line's reader", func(struct{}) {
		if _, err := fmt.Fprintln(stdout, lockclient.LockedLine); err != nil {
			log.Printf("lock: tell the shell the session is locked: %v", err)
		}
	})
	defer func() { lockedLine.drain(time.After(drainWait)) }()
	logindLife, endLogind := context.WithCancel(context.Background())
	defer endLogind()
	ld := connectLogind(logindLife)
	hints := newLatest("logind", func(locked bool) {
		client := ld.wait()
		if client == nil {
			return
		}
		ctx, cancel := context.WithTimeout(logindLife, logind.CallTimeout)
		defer cancel()
		if err := client.SetLockedHint(ctx, locked); err != nil {
			log.Printf("lock: SetLockedHint failed (non-fatal): %v", err)
		}
	})
	defer func() { hints.drain(time.After(drainWait)) }()
	deps := Deps{
		Locker: appLocker{a: a},
		Loop:   a,
		Now:    wall.Now,
		Wall:   wall,
		NewTimer: func(fn func()) Timer {
			t := time.AfterFunc(time.Hour, func() { a.Invoke(fn) })
			t.Stop()
			return runtimeTimer{t}
		},
		Auth: func(service string, onEvent func(auth.Event)) authHandle {
			return auth.Spawn(auth.NewPAM(service, verified), auth.CurrentUsername(), onEvent)
		},
		Decode:   imagedecode.OpenOffHeap,
		Outputs:  func() []image.Point { return outputsize.Devices(sess.Outputs()) },
		Forget:   a.ForgetImage,
		OnLocked: lockedLine.set,
		OnEnded:  a.Quit,
		OnCapsLock: func(fn func(on bool)) func() {
			return a.OnLocks(func(l app.Locks) { fn(l.Caps) })
		},
		SetLockedHint: hints.set,
	}
	account := sessionAccount()
	s := New(cfg, account.RealName, app.FontFallback(base), apptheme.New(cfg).Sheet, deps)
	scale := loginscreen.MaxScale(sess.Outputs())
	go func() {
		if img := loginscreen.AvatarPicture(imagedecode.Open, account.Avatar, scale); img != nil {
			a.Invoke(func() { s.SetPicture(img) })
		}
	}()
	go func() {
		client := ld.wait()
		if client == nil {
			return
		}
		if err := client.Listen(logindLife, func() {}, func() { a.Invoke(s.ForceUnlock) }); err != nil {
			log.Printf("lock: logind Unlock unavailable: %v", err)
		}
	}()
	// The lock is requested before the loop runs: Run returns at once
	// while there is neither a window nor a lock to drive.
	if err := s.Lock(); err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	if err := a.Run(); err != nil && !errors.Is(err, app.ErrClosed) {
		return fmt.Errorf("lock: %w", err)
	}
	if !s.unlocked {
		return ErrRefused
	}
	return nil
}

// sessionAccount is the account the lock asks for. A lock must lock even
// when the account cannot be looked up, so that shows the login name
// with no picture.
func sessionAccount() useraccount.Account {
	account, err := useraccount.Current()
	if err != nil {
		name := auth.CurrentUsername()
		log.Printf("lock: %v; showing the login name %q without a picture", err, name)
		return useraccount.Account{Name: name, RealName: name}
	}
	return account
}

// pendingLogind is logind's client being connected off the way of the lock: a
// system bus that does not answer must not hold the session unlocked, so the lock
// goes ahead and what needs logind waits for it, at most logind.CallTimeout.
type pendingLogind struct {
	done   chan struct{}
	client *logind.Client
}

func connectLogind(life context.Context) *pendingLogind {
	p := &pendingLogind{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(life, logind.CallTimeout)
		defer cancel()
		p.client = logind.SystemClient(ctx, "lock")
	}()
	return p
}

// wait is the client once connected, nil without logind.
func (p *pendingLogind) wait() *logind.Client {
	<-p.done
	return p.client
}

// runtimeTimer is a time.Timer as a Timer.
type runtimeTimer struct{ t *time.Timer }

func (r runtimeTimer) Reset(d time.Duration) { r.t.Reset(d) }
func (r runtimeTimer) Stop()                 { r.t.Stop() }

// holdSession takes the session's lys-login lock file under env's
// $XDG_RUNTIME_DIR/lys-login, held until the returned release; another
// holder is ErrHeld. Without a runtime dir there is no session to share
// it with: the lock goes ahead unguarded, and the log says so.
func holdSession(env xdg.Env) (release func(), err error) {
	dir, ok := env.RuntimeDir("lys-login")
	if !ok {
		log.Print("lock: XDG_RUNTIME_DIR is not an absolute path; locking without the one-lock-per-session guard")
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lock: create %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock.lock"), os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // our own runtime file
	if err != nil {
		return nil, fmt.Errorf("lock: open the session lock file: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("lock: take the session lock file: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
