package dm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/peercred"

	"github.com/stubbedev/lys-login/internal/cursorrecord"
	"github.com/stubbedev/lys-login/internal/greeterconfig"
)

// DefaultRuntimeDir is where the daemon keeps its sockets and the
// kiosk config unless told otherwise.
const DefaultRuntimeDir = "/run/lys-login"

// ErrNoDaemon is no display manager listening for a request.
var ErrNoDaemon = errors.New("lys-login's display manager is not running")

// Socket is one of the display manager's sockets in its runtime
// directory, open to every local user, taking one request of type T
// per connection. The daemon decides by the peer's uid what it does
// with a request.
type Socket[T any] struct {
	name string
	// maxRequest bounds a request on the wire.
	maxRequest int64
}

var (
	// ApplySocket takes a change of the login screen from `lys-login
	// apply-config`, from root and from the user whose session runs.
	ApplySocket = Socket[greeterconfig.Change]{
		name:       "apply.sock",
		maxRequest: (greeterconfig.MaxStaged+greeterconfig.MaxImage)/3*4 + 4096,
	}
	// CursorSocket takes the cursor of the session that runs from
	// `lys-login record-cursor`, for the greeter to show next.
	CursorSocket = Socket[cursorrecord.Cursor]{name: "cursor.sock", maxRequest: 4096}
	SplashSocket = Socket[Presented]{name: "splash.sock", maxRequest: 64}
)

type Presented struct{}

const RuntimeDirEnv = "LYS_LOGIN_RUNTIME_DIR"

// Path is the socket in runtimeDir.
func (s Socket[T]) Path(runtimeDir string) string { return filepath.Join(runtimeDir, s.name) }

// maxReply bounds the daemon's answer.
const maxReply = 64 << 10

// exchangeTimeout bounds one exchange on a socket.
const exchangeTimeout = 30 * time.Second

// reply is the daemon's answer: the file the request went to, or why
// it was refused.
type reply struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// noOwner is a sessionOwner with no session running.
const noOwner = -1

// sessionOwner is the uid of the session the daemon runs, noOwner while
// the greeter is up.
type sessionOwner struct{ uid atomic.Int64 }

func newSessionOwner() *sessionOwner {
	o := &sessionOwner{}
	o.uid.Store(noOwner)
	return o
}

func (o *sessionOwner) set(uid uint32) { o.uid.Store(int64(uid)) }
func (o *sessionOwner) clear()         { o.uid.Store(noOwner) }

// runs is whether uid's session runs.
func (o *sessionOwner) runs(uid uint32) bool { return int64(uid) == o.uid.Load() }

// mayApply is whether uid may change the login screen: root, and the
// user whose session runs.
func (o *sessionOwner) mayApply(uid uint32) bool { return uid == 0 || o.runs(uid) }

// listen binds the socket in runtimeDir, open to every local user;
// serve's handler decides who may do anything.
func (s Socket[T]) listen(runtimeDir string) (*net.UnixListener, error) {
	path := s.Path(runtimeDir)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remove the stale %s: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // every local user may ask; the handler checks who
		_ = ln.Close()
		return nil, fmt.Errorf("open %s to local users: %w", path, err)
	}
	return ln, nil
}

// handler carries out a request from uid and returns the file it went
// to, or the refusal the client sees.
type handler[T any] func(uid uint32, req T) (string, error)

// serve answers each connection on ln with handle until ln closes.
func (s Socket[T]) serve(ln *net.UnixListener, handle handler[T]) {
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			if err := s.answer(conn, handle); err != nil {
				log.Printf("dm: %s: %v", s.name, err)
			}
		}()
	}
}

func (s Socket[T]) answer(conn *net.UnixConn, handle handler[T]) error {
	_ = conn.SetDeadline(time.Now().Add(exchangeTimeout))
	uid, err := peercred.UID(conn)
	if err != nil {
		return err
	}
	var req T
	if err := json.NewDecoder(io.LimitReader(conn, s.maxRequest)).Decode(&req); err != nil {
		return send(conn, reply{Error: "malformed request: " + err.Error()}, fmt.Errorf("decode uid %d's request: %w", uid, err))
	}
	path, err := handle(uid, req)
	if err != nil {
		return send(conn, reply{Error: err.Error()}, fmt.Errorf("refused uid %d: %w", uid, err))
	}
	return send(conn, reply{Path: path}, nil)
}

// send writes r, returning cause, or the write's failure without one.
func send(conn net.Conn, r reply, cause error) error {
	if err := json.NewEncoder(conn).Encode(r); err != nil && cause == nil {
		return fmt.Errorf("reply: %w", err)
	}
	return cause
}

// Send hands req to the display manager whose runtime directory is
// runtimeDir and returns the file it went to; ErrNoDaemon without one.
func (s Socket[T]) Send(runtimeDir string, req T) (string, error) {
	path := s.Path(runtimeDir)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return "", fmt.Errorf("%w (no %s)", ErrNoDaemon, path)
	}
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(exchangeTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return "", fmt.Errorf("send to %s: %w", path, err)
	}
	if err := conn.CloseWrite(); err != nil {
		return "", fmt.Errorf("send to %s: %w", path, err)
	}
	var r reply
	if err := json.NewDecoder(io.LimitReader(conn, maxReply)).Decode(&r); err != nil {
		return "", fmt.Errorf("read the display manager's answer on %s: %w", path, err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("the display manager refused: %s", r.Error)
	}
	return r.Path, nil
}

// applyChange writes the changes owner allows to the greeter's config
// at configPath, one at a time.
func applyChange(owner *sessionOwner, configPath string) handler[greeterconfig.Change] {
	var mu sync.Mutex
	return func(uid uint32, c greeterconfig.Change) (string, error) {
		if !owner.mayApply(uid) {
			return "", errors.New("only root and the user logged in here may change the login screen")
		}
		mu.Lock()
		defer mu.Unlock()
		path, err := greeterconfig.Write(c, configPath)
		if err != nil {
			return "", err
		}
		log.Printf("dm: uid %d changed the login screen", uid)
		return path, nil
	}
}

func TakeScreenFromSplash(runtimeDir string) func() {
	if runtimeDir == "" {
		return nil
	}
	return func() {
		if _, err := SplashSocket.Send(runtimeDir, Presented{}); err != nil {
			log.Printf("greeter: take the screen from the splash: %v", err)
		}
	}
}

func handOverSplash(isGreeter func(uid uint32) bool, quit func() error) handler[Presented] {
	var once sync.Once
	return func(uid uint32, _ Presented) (string, error) {
		if uid != 0 && !isGreeter(uid) {
			return "", errors.New("only the greeter may take the screen from the splash")
		}
		once.Do(func() {
			if err := quit(); err != nil {
				log.Printf("dm: quit the splash: %v", err)
			}
		})
		return "", nil
	}
}

func quitSplash(splash string) func() error {
	return func() error {
		if splash == "" {
			return nil
		}
		out, err := exec.Command(splash, "quit", "--retain-splash").CombinedOutput()
		switch {
		case err == nil, errors.Is(err, fs.ErrNotExist), errors.Is(err, exec.ErrNotFound):
			return nil
		case len(out) > 0:
			return fmt.Errorf("%s quit --retain-splash: %w: %s", splash, err, strings.TrimSpace(string(out)))
		default:
			return fmt.Errorf("%s quit --retain-splash: %w", splash, err)
		}
	}
}

// recordCursor writes the cursor of the session owner runs to its
// user's record under dir.
func recordCursor(owner *sessionOwner, dir string) handler[cursorrecord.Cursor] {
	return func(uid uint32, c cursorrecord.Cursor) (string, error) {
		if !owner.runs(uid) {
			return "", errors.New("only the user logged in here may record their session's cursor")
		}
		return cursorrecord.Write(dir, uid, c)
	}
}
