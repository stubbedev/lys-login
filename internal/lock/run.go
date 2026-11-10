package lock

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/imagedecode"
	"github.com/stubbedev/lys-desktop/logging"
	"github.com/stubbedev/lys-desktop/outputsize"
	"github.com/stubbedev/lys-desktop/shell/apptheme"
	"github.com/stubbedev/lys-desktop/shell/lockclient"
	"github.com/stubbedev/lys-desktop/useraccount"
	"github.com/stubbedev/lys-lib/logind"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-ui/app"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/dm"
	"github.com/stubbedev/lys-login/internal/greeter"
	"github.com/stubbedev/lys-login/internal/layouts"
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
	ld := connectLogind()
	defer ld.close()
	hints := newLatest("logind", func(locked bool) {
		session := ld.wait()
		if session == nil {
			return
		}
		if err := session.SetLockedHint(ld.life, locked); err != nil {
			log.Printf("lock: SetLockedHint failed (non-fatal): %v", err)
		}
	})
	defer func() { hints.drain(time.After(drainWait)) }()
	watches := &busWatches{invoke: a.Invoke}
	defer watches.wait()
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
		SetLockedHint:    hints.set,
		Media:            newSessionMedia(watches, cfg.Media),
		Battery:          watches.battery,
		Fingerprint:      watches.fingerprint(auth.CurrentUsername()),
		OnKeyboardLayout: a.OnKeyboardLayout,
		NextLayout:       layouts.Switcher("lock"),
		LabelLayouts:     layouts.LabeledOnSystem,
		SwitchUser:       switchUser(cfg.Login.Lock.ShowSwitchUser),
	}
	account := sessionAccount()
	s := New(cfg, account.RealName, app.FontFallback(base), apptheme.New(cfg).Sheet, deps)
	defer s.stopWatches()
	scale := loginscreen.MaxScale(sess.Outputs())
	go func() {
		if img := loginscreen.AvatarPicture(imagedecode.Open, account.Avatar, scale); img != nil {
			a.Invoke(func() { s.SetPicture(img) })
		}
	}()
	ld.follow(func(e logind.Event) {
		if _, ok := e.(logind.Unlocked); ok {
			a.Invoke(s.ForceUnlock)
		}
	})
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

// pendingLogind is the user's logind session being resolved off the way of
// the lock: a system bus that does not answer must not hold the session
// unlocked, so the lock goes ahead and what needs logind waits for it, each
// step bounded by logind.CallTimeout. Its calls and watches end with close.
type pendingLogind struct {
	life      context.Context
	end       context.CancelFunc
	done      chan struct{}
	followers sync.WaitGroup
	conn      *dbus.Conn
	session   *logind.Session
}

func connectLogind() *pendingLogind {
	life, end := context.WithCancel(context.Background())
	p := &pendingLogind{life: life, end: end, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		conn, err := logind.Dial(life)
		if err != nil {
			log.Printf("lock: logind unavailable: %v", err)
			return
		}
		session, err := logind.NewManager(conn).GetSession(life, logind.AutoSession)
		if err != nil {
			log.Printf("lock: logind unavailable: %v", err)
			_ = conn.Close()
			return
		}
		p.conn, p.session = conn, &session
	}()
	return p
}

// wait is the session once resolved, nil without logind.
func (p *pendingLogind) wait() *logind.Session {
	<-p.done
	return p.session
}

// follow runs handle, on a watch goroutine, for every signal logind sends
// about the session until close.
func (p *pendingLogind) follow(handle func(logind.Event)) {
	p.followers.Go(func() {
		session := p.wait()
		if session == nil {
			return
		}
		w, err := session.Watch(p.life, func(e logind.Event, err error) {
			if err != nil {
				log.Printf("lock: %v", err)
				return
			}
			handle(e)
		})
		if err != nil {
			log.Printf("lock: logind Unlock unavailable: %v", err)
			return
		}
		<-w.Done()
		if err := w.Stop(); err != nil {
			log.Printf("lock: logind Unlock no longer followed: %v", err)
		}
	})
}

// close ends every call and watch and drops the connection.
func (p *pendingLogind) close() {
	p.end()
	p.followers.Wait()
	<-p.done
	if p.conn != nil {
		_ = p.conn.Close()
	}
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

func switchUser(shown bool) func() {
	if !shown {
		return nil
	}
	if users := greeter.ReadUsers(); len(users) < 2 {
		return nil
	}
	if _, err := os.Stat(dm.SwitchSocket.Path(dmsocket.DefaultRuntimeDir)); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("lock: no Switch user: %v", err)
		}
		return nil
	}
	return func() {
		go func() {
			if _, err := dm.SwitchSocket.Send(dmsocket.DefaultRuntimeDir, dm.SwitchRequest{}); err != nil {
				log.Printf("lock: switch user: %v", err)
			}
		}()
	}
}
