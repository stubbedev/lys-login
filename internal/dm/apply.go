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
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/peercred"

	"github.com/stubbedev/lys-login/internal/greeterconfig"
)

// DefaultRuntimeDir is where the daemon keeps its sockets and the
// kiosk config unless told otherwise.
const DefaultRuntimeDir = "/run/lys-login"

// ApplySocket is the socket under runtimeDir that `lys-login
// apply-config` hands a user's change of the login screen to.
func ApplySocket(runtimeDir string) string { return filepath.Join(runtimeDir, "apply.sock") }

// ErrNoDaemon is no display manager listening for a change.
var ErrNoDaemon = errors.New("lys-login's display manager is not running")

// maxApplyRequest bounds a change on the wire: the staged config and
// the image, base64 in JSON.
const maxApplyRequest = (greeterconfig.MaxStaged+greeterconfig.MaxImage)/3*4 + 4096

// maxApplyReply bounds the daemon's answer.
const maxApplyReply = 64 << 10

// applyTimeout bounds one exchange on the apply socket.
const applyTimeout = 30 * time.Second

// applyReply is the daemon's answer: where the change went, or why it
// was refused.
type applyReply struct {
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

// mayApply is whether uid may change the login screen: root, and the
// user whose session runs.
func (o *sessionOwner) mayApply(uid uint32) bool {
	return uid == 0 || int64(uid) == o.uid.Load()
}

// listenApply binds the apply socket at path, open to every local user;
// serveApply decides who may change anything.
func listenApply(path string) (*net.UnixListener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remove the stale %s: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o666); err != nil { //nolint:gosec // every local user may ask; serveApply checks who
		_ = ln.Close()
		return nil, fmt.Errorf("open %s to local users: %w", path, err)
	}
	return ln, nil
}

// serveApply writes the changes owner allows to the greeter's config
// at configPath, one at a time, until ln closes.
func serveApply(ln *net.UnixListener, owner *sessionOwner, configPath string) {
	var mu sync.Mutex
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			if err := handleApply(conn, owner, configPath, &mu); err != nil {
				log.Printf("dm: apply: %v", err)
			}
		}()
	}
}

func handleApply(conn *net.UnixConn, owner *sessionOwner, configPath string, mu *sync.Mutex) error {
	_ = conn.SetDeadline(time.Now().Add(applyTimeout))
	uid, err := peercred.UID(conn)
	if err != nil {
		return err
	}
	var c greeterconfig.Change
	if err := json.NewDecoder(io.LimitReader(conn, maxApplyRequest)).Decode(&c); err != nil {
		return replyApply(conn, applyReply{Error: "malformed change"}, fmt.Errorf("decode uid %d's change: %w", uid, err))
	}
	if !owner.mayApply(uid) {
		return replyApply(conn, applyReply{Error: "only root and the user logged in here may change the login screen"},
			fmt.Errorf("refused uid %d, who has no session here", uid))
	}
	mu.Lock()
	path, err := greeterconfig.Write(c, configPath)
	mu.Unlock()
	if err != nil {
		return replyApply(conn, applyReply{Error: err.Error()}, fmt.Errorf("uid %d's change: %w", uid, err))
	}
	log.Printf("dm: apply: uid %d changed the login screen", uid)
	return replyApply(conn, applyReply{Path: path}, nil)
}

// replyApply sends r, returning cause, or the send's failure without
// one.
func replyApply(conn net.Conn, r applyReply, cause error) error {
	if err := json.NewEncoder(conn).Encode(r); err != nil && cause == nil {
		return fmt.Errorf("reply: %w", err)
	}
	return cause
}

// SendChange hands c to the display manager at the apply socket path
// and returns the runtime.yaml it wrote; ErrNoDaemon without one.
func SendChange(path string, c greeterconfig.Change) (string, error) {
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return "", fmt.Errorf("%w (no %s)", ErrNoDaemon, path)
	}
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(applyTimeout))
	if err := json.NewEncoder(conn).Encode(c); err != nil {
		return "", fmt.Errorf("send the change: %w", err)
	}
	if err := conn.CloseWrite(); err != nil {
		return "", fmt.Errorf("send the change: %w", err)
	}
	var r applyReply
	if err := json.NewDecoder(io.LimitReader(conn, maxApplyReply)).Decode(&r); err != nil {
		return "", fmt.Errorf("read the display manager's answer: %w", err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("the display manager refused the change: %s", r.Error)
	}
	return r.Path, nil
}
