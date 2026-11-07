package dm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/xkb"
	"github.com/stubbedev/lys-shell/config"
	"github.com/stubbedev/lys-shell/shlex"
	"golang.org/x/sys/unix"

	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/greeter"
	"github.com/stubbedev/lys-login/internal/keyboard"
)

// Options configure the daemon.
type Options struct {
	WorkerOptions
	// GreeterUser is the unprivileged account the greeter runs as.
	GreeterUser string
	// Compositor is the greeter's compositor command, lys-wm; the daemon
	// adds its kiosk config.
	Compositor []string
	// GreeterArgs follow `lys-login greet`: the config, the state file,
	// the session directories.
	GreeterArgs []string
	// RuntimeDir holds the greeter's socket, ApplySocket, CursorSocket
	// and the kiosk config.
	RuntimeDir string
	// GreeterConfig is the system config the greeter reads, whose
	// runtime.yaml the logged-in user's changes go to.
	GreeterConfig string
	// GreeterState is the greeter's state file, beside which it
	// remembers the last user.
	GreeterState string
	// CursorDir holds the cursor records the sessions hand over.
	CursorDir string
	Keyboard  keyboard.Sources
	Splash    string
	Args      []string
}

// retryPause spaces greeters that fail to come up.
const retryPause = 2 * time.Second

// StopSignals end a daemon: systemd stops one with SIGTERM and, as the service
// sets SendSIGHUP, SIGHUP right after it, whose default would kill the daemon
// before it could end the session.
var StopSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// Run shows the greeter on the VT and starts each session a user logs
// in to, until ctx ends. Meanwhile it takes changes of the login screen
// on ApplySocket from root and from the user whose session runs, and
// that session's cursor on CursorSocket.
func Run(ctx context.Context, o Options) error {
	return runDaemon(ctx, o, systemConsole(), rootOnly, nil)
}

type console struct {
	activate func(vt int) error
	// showText gives the VT's console its cursor back (true), or clears it and
	// hides the cursor (false).
	showText func(vt int, on bool) error
	cards    *heldCards
	stop     func(worker int) error
	freeVT   func() (int, io.Closer, error)
	release  func(vt int) error
	lock     func(session string) error
	switchTo func(session string) error
}

func systemConsole() console {
	return console{
		activate: activateVT, showText: showVTText, cards: newHeldCards(), stop: stopWorker,
		freeVT: freeVT, release: disallocateVT, lock: lockLogindSession, switchTo: activateLogindSession,
	}
}

// hideText leaves nothing on the kernel's console to see. The console shows
// whenever no compositor holds the VT, and logind decides when that starts, so
// its mode cannot be held: its contents can.
func (c console) hideText(vt int) {
	if err := c.showText(vt, false); err != nil {
		log.Printf("dm: %v", err)
	}
}

type listeners struct {
	apply, cursor, splash, upgrade, switchUser *net.UnixListener
}

func listen(runtimeDir string) (l *listeners, err error) {
	l = &listeners{}
	if l.apply, err = ApplySocket.listen(runtimeDir); err != nil {
		return nil, err
	}
	if l.cursor, err = CursorSocket.listen(runtimeDir); err != nil {
		return nil, errors.Join(err, l.close())
	}
	if l.splash, err = SplashSocket.listen(runtimeDir); err != nil {
		return nil, errors.Join(err, l.close())
	}
	if l.upgrade, err = UpgradeSocket.listen(runtimeDir); err != nil {
		return nil, errors.Join(err, l.close())
	}
	if l.switchUser, err = SwitchSocket.listen(runtimeDir); err != nil {
		return nil, errors.Join(err, l.close())
	}
	return l, nil
}

func (l *listeners) close() error {
	var errs []error
	for _, ln := range []*net.UnixListener{l.apply, l.cursor, l.splash, l.upgrade, l.switchUser} {
		if ln != nil {
			errs = append(errs, ln.Close())
		}
	}
	return errors.Join(errs...)
}

func stopWorker(worker int) error { return syscall.Kill(worker, syscall.SIGTERM) }

// The console ioctls of linux/vt.h, which x/sys/unix does not carry.
const (
	vtActivate   = 0x5606
	vtWaitActive = 0x5607
)

// activateVT switches the console to vt and waits until it shows.
func activateVT(vt int) error {
	tty, err := os.OpenFile(fmt.Sprintf("/dev/tty%d", vt), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open VT %d: %w", vt, err)
	}
	defer func() { _ = tty.Close() }()
	fd := int(tty.Fd())
	if err := unix.IoctlSetInt(fd, vtActivate, vt); err != nil {
		return fmt.Errorf("activate VT %d: %w", vt, err)
	}
	if err := unix.IoctlSetInt(fd, vtWaitActive, vt); err != nil {
		return fmt.Errorf("wait for VT %d: %w", vt, err)
	}
	return nil
}

// The console sequences: clear the screen and its scrollback, home, and hide
// or show the cursor.
const (
	vtHideText = "\x1b[2J\x1b[3J\x1b[H\x1b[?25l"
	vtShowText = "\x1b[?25h"
)

// showVTText writes the sequence to the VT's console.
func showVTText(vt int, on bool) error {
	tty, err := os.OpenFile(fmt.Sprintf("/dev/tty%d", vt), os.O_WRONLY|syscall.O_NOCTTY, 0) //nolint:gosec // the VT the daemon was told to run on
	if err != nil {
		return fmt.Errorf("open VT %d: %w", vt, err)
	}
	defer func() { _ = tty.Close() }()
	sequence := vtHideText
	if on {
		sequence = vtShowText
	}
	if _, err := tty.WriteString(sequence); err != nil {
		return fmt.Errorf("write to VT %d: %w", vt, err)
	}
	return nil
}

// kioskLook is the part of lys-wm's default look a kiosk turns off: no
// border, gaps, rounding or shadow, so the greeter fills the screen to
// its edges, and no animations, so its first frame on screen is the
// whole greeter, and the hand-over from the splash shows nothing between
// the two, not lys-wm's empty background through a fade.
const kioskLook = "borders:\n  width: 0\ngaps:\n  inner: 0\n  outer: 0\ndecoration:\n  rounding: 0\n  shadow:\n    enabled: false\nanimations:\n  enabled: false\n"

type kiosk struct {
	greet    []string
	cursor   greeter.Cursor
	keyboard xkb.Names
	a11y     greeter.Accessibility
}

// kioskConfig is lys-wm's config for the greeter: no binds, so the
// greeter's keys reach only it, kioskLook, the system keyboard layout,
// the greeter as the one program, and the cursor in the environment, from
// which lys-wm draws the cursor the greeter asks for and which the greeter
// inherits.
func kioskConfig(k kiosk) string {
	quoted := make([]string, len(k.greet))
	for i, arg := range k.greet {
		quoted[i] = shlex.MustQuote(arg)
	}
	var b strings.Builder
	b.WriteString("binds: []\n" + kioskLook)
	writeKeyboard(&b, k.keyboard, k.a11y)
	b.WriteString("env:\n  XCURSOR_SIZE: " + yamlString(strconv.FormatUint(uint64(k.cursor.Size), 10)) + "\n")
	if k.cursor.Theme != "" {
		b.WriteString("  XCURSOR_THEME: " + yamlString(k.cursor.Theme) + "\n")
	}
	b.WriteString("autostart:\n  - " + yamlString(strings.Join(quoted, " ")) + "\n")
	return b.String()
}

func writeKeyboard(b *strings.Builder, names xkb.Names, a11y greeter.Accessibility) {
	keys := a11y.WMConfig()
	if names == (xkb.Names{}) && keys == "" {
		return
	}
	b.WriteString("input:\n  keyboard:\n" + keys)
	for _, f := range [...]struct{ key, value string }{{"model", names.Model}, {"layout", names.Layout}, {"variant", names.Variant}, {"options", names.Options}} {
		if f.value != "" {
			b.WriteString("    " + f.key + ": " + yamlString(f.value) + "\n")
		}
	}
}

func (o Options) greeterKeyboard() xkb.Names {
	r, err := o.Keyboard.Read(func(err error) {
		log.Printf("dm: %v; the greeter's keyboard layout comes from %s, else %s", err, o.Keyboard.Xorg, o.Keyboard.Vconsole)
	})
	if err != nil {
		log.Printf("dm: the greeter types with lys-wm's default keymap: %v", err)
		return xkb.Names{}
	}
	if err := keyboard.Check(r.Names); err != nil {
		log.Printf("dm: the greeter types with lys-wm's default keymap: the layout from %s: %v", r.From, err)
		return xkb.Names{}
	}
	return r.Names
}

func (o Options) greeterAccessibility() greeter.Accessibility {
	a, err := greeter.LoadAccessibility(o.GreeterState)
	if err != nil {
		log.Printf("dm: the greeter starts without sticky or slow keys: %v", err)
	}
	return a
}

// greeterCursor is the cursor the next greeter shows: the greeter's
// config, else the record the remembered user's last session left.
func (o Options) greeterCursor() greeter.Cursor {
	cfg, err := config.LoadGreeter(o.GreeterConfig, config.StderrDiagnostics)
	if err != nil {
		log.Printf("dm: the greeter's config: %v", err)
	}
	return greeter.KioskCursor(cfg.Login.Greeter, o.CursorDir, greeter.ReadUsers(), greeter.LoadLast(greeter.LastUserPath(o.GreeterState)))
}

// yamlString is s as a double-quoted YAML scalar.
func yamlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// pendingLogin is an authenticated login waiting for its session.
type pendingLogin struct {
	w        *worker
	user     string
	existing string
	cmd, env []string
}

type greeted struct {
	login   *pendingLogin
	upgrade *pendingUpgrade
}

type greeterRequests struct {
	vt       int
	upgrades <-chan *pendingUpgrade
	drain    time.Duration
	existing func(user string) bool
}

// greet runs the greeter until a user is authenticated and asks for a
// session, or an upgrade replaces this program, then stops it.
func (o Options) greet(ctx context.Context, vt int, upgrades <-chan *pendingUpgrade, drain time.Duration, existing func(user string) bool) (greeted, error) {
	req := greeterRequests{vt: vt, upgrades: upgrades, drain: drain, existing: existing}
	sock := filepath.Join(o.RuntimeDir, "greeter.sock")
	ln, err := o.listen(sock)
	if err != nil {
		return greeted{}, err
	}
	defer func() { _ = ln.Close() }()
	cfg := filepath.Join(o.RuntimeDir, "kiosk.yaml")
	if err := os.WriteFile(cfg, []byte(kioskConfig(kiosk{greet: o.greeterCommand(), cursor: o.greeterCursor(), keyboard: o.greeterKeyboard(), a11y: o.greeterAccessibility()})), 0o644); err != nil { //nolint:gosec // the greeter user reads it
		return greeted{}, fmt.Errorf("write %s: %w", cfg, err)
	}
	gw := exec.Command(o.Self, o.greeterWorkerArgs(sock, cfg, vt)...) //nolint:gosec // lys-login itself
	gw.Stdout, gw.Stderr = os.Stderr, os.Stderr
	if err := gw.Start(); err != nil {
		return greeted{}, fmt.Errorf("start the greeter worker: %w", err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = gw.Wait()
		close(exited)
	}()
	conns, done := make(chan net.Conn), make(chan struct{})
	defer close(done)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case conns <- c:
			case <-done:
				_ = c.Close()
				return
			}
		}
	}()
	g, err := o.serveConns(ctx, conns, exited, req)
	_ = gw.Process.Signal(syscall.SIGTERM)
	<-exited
	if g == (greeted{}) && err == nil {
		err = errors.Join(errors.New("the greeter ended"), exitErr)
	}
	return g, err
}

func (o Options) greeterCommand() []string {
	return append([]string{o.Self, "greet", "--hand-over"}, o.GreeterArgs...)
}

func (o Options) greeterWorkerArgs(sock, kiosk string, vt int) []string {
	args := []string{
		"worker", "greeter", "--service", o.Service, "--vt", strconv.Itoa(vt), "--user", o.GreeterUser,
		"--env", auth.GreetdSockEnv + "=" + sock, "--env", RuntimeDirEnv + "=" + o.RuntimeDir, "--",
	}
	return append(append(args, o.Compositor...), "--config", kiosk)
}

func (o Options) isGreeter(uid uint32) bool {
	a, err := lookupAccount(o.GreeterUser)
	return err == nil && a.uid == uid
}

// serveConns serves the greeter's connections, each on its own, as the
// greeter opens one per login attempt, until one hands over a session to
// start, the greeter's worker ends (neither a login nor an error), ctx
// does, or an upgrade that passed its check has let the logins under way
// finish. The connections still open then are closed, which cancels their
// logins.
func (o Options) serveConns(ctx context.Context, conns <-chan net.Conn, exited <-chan struct{}, r greeterRequests) (greeted, error) {
	logins := make(chan *pendingLogin)
	open := map[net.Conn]bool{}
	ended := make(chan net.Conn)
	defer func() {
		for c := range open {
			_ = c.Close()
		}
		for remaining := len(open); remaining > 0; {
			select {
			case <-ended:
				remaining--
			case late := <-logins:
				// A second login that finished at the same moment
				// starts nothing.
				late.w.cancel()
			}
		}
	}()
	var attempt *pendingUpgrade
	var drained <-chan time.Time
	for {
		requests := r.upgrades
		if attempt != nil {
			requests = nil
		}
		select {
		case <-ctx.Done():
			return greeted{upgrade: attempt}, ctx.Err()
		case <-exited:
			return greeted{upgrade: attempt}, nil
		case req := <-requests:
			if len(open) == 0 {
				return greeted{upgrade: req}, nil
			}
			log.Printf("dm: an upgrade waits for the logins under way, %s at most", r.drain)
			attempt, drained = req, time.After(r.drain)
		case <-drained:
			return greeted{upgrade: attempt}, nil
		case conn := <-conns:
			open[conn] = true
			go func() {
				login, err := o.serveGreeter(conn, r)
				_ = conn.Close()
				switch {
				case login != nil:
					logins <- login
				case err != nil:
					log.Printf("dm: greeter connection: %v", err)
				}
				ended <- conn
			}()
		case login := <-logins:
			// The winning connection's goroutine still reports its end.
			return greeted{login: login, upgrade: attempt}, nil
		case conn := <-ended:
			delete(open, conn)
			if attempt != nil && len(open) == 0 {
				return greeted{upgrade: attempt}, nil
			}
		}
	}
}

// listen binds the greeter's socket, reachable by the greeter user alone.
func (o Options) listen(path string) (*net.UnixListener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove the stale %s: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	a, err := lookupAccount(o.GreeterUser)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	if err := os.Chown(path, int(a.uid), int(a.gid)); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("give %s to %s: %w", path, o.GreeterUser, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("restrict %s: %w", path, err)
	}
	return ln, nil
}

// serveGreeter answers the greeter's greetd requests, relaying each
// login to a session worker, until a login asks to start its session or
// the greeter goes away.
func (o Options) serveGreeter(conn net.Conn, r greeterRequests) (*pendingLogin, error) {
	var w *worker
	state := endedLogin
	user, existing := "", ""
	defer func() {
		if w != nil && state != authenticatedLogin {
			w.cancel()
		}
	}()
	// step relays the worker's next event and forgets a worker that
	// ended.
	step := func() error {
		var err error
		state, err = relay(conn, w)
		if state == endedLogin {
			w = nil
		}
		return err
	}
	for {
		req, err := auth.ReadGreetdRequest(conn)
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read the greeter's request: %w", err)
		}
		switch req.Type {
		case auth.GreetdCreateSession:
			if w != nil {
				w.cancel()
			}
			w, user, existing = nil, req.Username, ""
			if r.existing(req.Username) {
				log.Printf("dm: %s already has a session: the greeter switches to it", req.Username)
				existing, state = req.Username, authenticatedLogin
				if err := auth.WriteGreetdSuccess(conn); err != nil {
					return nil, err
				}
				continue
			}
			if w, err = o.startSessionWorker(req.Username, r.vt); err != nil {
				w, state = nil, endedLogin
				if err := auth.WriteGreetdError(conn, false, err.Error()); err != nil {
					return nil, err
				}
				continue
			}
			if err := step(); err != nil {
				return nil, err
			}
		case auth.GreetdPostResponse:
			if w == nil || state != awaitingAnswer {
				if err := auth.WriteGreetdError(conn, false, "no login is waiting for an answer"); err != nil {
					return nil, err
				}
				continue
			}
			if err := w.ch.send(request{Op: opAnswer, Answer: req.Response}); err != nil {
				return nil, err
			}
			if err := step(); err != nil {
				return nil, err
			}
		case auth.GreetdCancelSession:
			if w != nil {
				w.cancel()
			}
			w, state, existing = nil, endedLogin, ""
			if err := auth.WriteGreetdSuccess(conn); err != nil {
				return nil, err
			}
		case auth.GreetdStartSession:
			if state != authenticatedLogin || len(req.Cmd) == 0 {
				if err := auth.WriteGreetdError(conn, false, "no authenticated login to start, or no command"); err != nil {
					return nil, err
				}
				continue
			}
			if err := auth.WriteGreetdSuccess(conn); err != nil {
				return nil, err
			}
			return &pendingLogin{w: w, user: user, existing: existing, cmd: req.Cmd, env: req.Env}, nil
		}
	}
}

// loginState is where a login stands after its worker's last event.
type loginState int

const (
	// awaitingAnswer: a prompt is with the greeter.
	awaitingAnswer loginState = iota
	authenticatedLogin
	// endedLogin: the login failed and its worker is gone.
	endedLogin
)

// relay forwards the worker's next event to the greeter: a prompt
// waits for the greeter's answer, authenticated is a success, a failure
// an authentication error that ends the worker.
func relay(conn net.Conn, w *worker) (loginState, error) {
	var ev event
	if err := w.ch.recv(&ev); err != nil {
		w.cancel()
		return endedLogin, auth.WriteGreetdError(conn, false, "the login ended: "+err.Error())
	}
	switch ev.Kind {
	case evPrompt:
		if ev.Prompt == nil {
			w.cancel()
			return endedLogin, errors.New("dm: a prompt event without a prompt")
		}
		return awaitingAnswer, auth.WriteGreetdPrompt(conn, *ev.Prompt)
	case evAuthenticated:
		return authenticatedLogin, auth.WriteGreetdSuccess(conn)
	case evFailed:
		w.wait()
		return endedLogin, auth.WriteGreetdError(conn, true, ev.Text)
	}
	w.cancel()
	return endedLogin, fmt.Errorf("dm: unexpected event %q while authenticating", ev.Kind)
}

type session struct {
	w    *worker
	cmd  []string
	uid  *uint32
	vt   int
	user string
	id   string
	stop chan struct{}
	held io.Closer
}

type workerEvent struct {
	ev  event
	err error
}

func (w *worker) events(stop <-chan struct{}) <-chan workerEvent {
	events := make(chan workerEvent)
	go func() {
		defer close(events)
		for {
			var ev event
			err := w.ch.recv(&ev)
			select {
			case events <- workerEvent{ev, err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return events
}

// worker is a session worker process and its channel.
type worker struct {
	proc *os.Process
	ch   *channel
	f    *os.File
}

// startSessionWorker starts a session worker and asks it to
// authenticate user.
func (o Options) startSessionWorker(user string, vt int) (*worker, error) {
	if user == "" {
		return nil, errors.New("no user name")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("socket pair: %w", err)
	}
	if err := unix.SetNonblock(pair[0], true); err != nil {
		_ = unix.Close(pair[0])
		_ = unix.Close(pair[1])
		return nil, fmt.Errorf("socket pair: %w", err)
	}
	mine, theirs := os.NewFile(uintptr(pair[0]), "dm"), os.NewFile(uintptr(pair[1]), "worker")
	defer func() { _ = theirs.Close() }()
	cmd := exec.Command(o.Self, "worker", "session", "--service", o.Service, "--vt", strconv.Itoa(vt), "--runtime-dir", o.RuntimeDir) //nolint:gosec // lys-login itself
	cmd.ExtraFiles = []*os.File{theirs}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		_ = mine.Close()
		return nil, fmt.Errorf("start a session worker: %w", err)
	}
	w := &worker{proc: cmd.Process, ch: newChannel(mine), f: mine}
	if err := w.ch.send(request{Op: opAuth, User: user}); err != nil {
		w.cancel()
		return nil, err
	}
	return w, nil
}

// cancel ends a worker that has not started a session.
func (w *worker) cancel() {
	_ = w.ch.send(request{Op: opCancel})
	w.wait()
}

// wait reaps the worker and closes the channel.
func (w *worker) wait() {
	_, _ = w.proc.Wait()
	_ = w.f.Close()
}
