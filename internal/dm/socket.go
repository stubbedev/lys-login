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
	exchange   time.Duration
}

var (
	// ApplySocket takes a change of the login screen from `lys-login
	// apply-config`, from root and from the user whose session runs.
	ApplySocket = Socket[greeterconfig.Change]{
		name:       "apply.sock",
		maxRequest: (greeterconfig.MaxStaged+greeterconfig.MaxImage)/3*4 + 4096,
		exchange:   exchangeTimeout,
	}
	// CursorSocket takes the cursor of the session that runs from
	// `lys-login record-cursor`, for the greeter to show next.
	CursorSocket  = Socket[cursorrecord.Cursor]{name: "cursor.sock", maxRequest: 4096, exchange: exchangeTimeout}
	SplashSocket  = Socket[Presented]{name: "splash.sock", maxRequest: 64, exchange: exchangeTimeout}
	UpgradeSocket = Socket[UpgradeRequest]{name: "upgrade.sock", maxRequest: 1 << 20, exchange: upgradeExchange}
	SwitchSocket  = Socket[SwitchRequest]{name: "switch.sock", maxRequest: 64, exchange: exchangeTimeout}
)

type Presented struct{}

type SwitchRequest struct{}

const RuntimeDirEnv = "LYS_LOGIN_RUNTIME_DIR"

// Path is the socket in runtimeDir.
func (s Socket[T]) Path(runtimeDir string) string { return filepath.Join(runtimeDir, s.name) }

// maxReply bounds the daemon's answer.
const maxReply = 64 << 10

// exchangeTimeout bounds one exchange on most sockets.
const exchangeTimeout = 30 * time.Second

// reply is the daemon's answer: the file the request went to, or why
// it was refused.
type reply struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

type sessionOwners struct {
	mu   sync.Mutex
	uids map[uint32]int
}

func newSessionOwners() *sessionOwners { return &sessionOwners{uids: map[uint32]int{}} }

func (o *sessionOwners) add(uid uint32) {
	o.mu.Lock()
	o.uids[uid]++
	o.mu.Unlock()
}

func (o *sessionOwners) remove(uid uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.uids[uid] <= 1 {
		delete(o.uids, uid)
		return
	}
	o.uids[uid]--
}

func (o *sessionOwners) runs(uid uint32) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.uids[uid] > 0
}

// mayApply is whether uid may change the login screen: root, and the
// user whose session runs.
func (o *sessionOwners) mayApply(uid uint32) bool { return uid == 0 || o.runs(uid) }

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
	_ = conn.SetDeadline(time.Now().Add(s.exchange))
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
	_ = conn.SetDeadline(time.Now().Add(s.exchange))
	// A daemon that refuses by who is asking answers without reading the request,
	// so a failed send can still leave its answer to read.
	sent := json.NewEncoder(conn).Encode(req)
	if sent == nil {
		sent = conn.CloseWrite()
	}
	var r reply
	if err := json.NewDecoder(io.LimitReader(conn, maxReply)).Decode(&r); err != nil {
		if sent != nil {
			return "", fmt.Errorf("send to %s: %w", path, sent)
		}
		return "", fmt.Errorf("read the display manager's answer on %s: %w", path, err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("the display manager refused: %s", r.Error)
	}
	return r.Path, nil
}

// applyChange writes the changes owner allows to the greeter's config
// at configPath, one at a time.
func applyChange(owner *sessionOwners, configPath string) handler[greeterconfig.Change] {
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

func gated[T any](gate *sync.RWMutex, handle handler[T]) handler[T] {
	return func(uid uint32, req T) (string, error) {
		gate.RLock()
		defer gate.RUnlock()
		return handle(uid, req)
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
func recordCursor(owner *sessionOwners, dir string) handler[cursorrecord.Cursor] {
	return func(uid uint32, c cursorrecord.Cursor) (string, error) {
		if !owner.runs(uid) {
			return "", errors.New("only the user logged in here may record their session's cursor")
		}
		return cursorrecord.Write(dir, uid, c)
	}
}

const switchQueueWait = 5 * time.Second

func askSwitch(owner *sessionOwners, switches chan<- struct{}) handler[SwitchRequest] {
	return func(uid uint32, _ SwitchRequest) (string, error) {
		if uid != 0 && !owner.runs(uid) {
			return "", errors.New("only root and the users logged in here may switch users")
		}
		select {
		case switches <- struct{}{}:
			log.Printf("dm: uid %d asked to switch users", uid)
			return "", nil
		case <-time.After(switchQueueWait):
			return "", fmt.Errorf("the daemon took no switch in %s", switchQueueWait)
		}
	}
}
