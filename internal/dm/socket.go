package dm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/peercred"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
	"github.com/stubbedev/lys-login/internal/cursorrecord"
)

// Socket is one of the display manager's sockets in its runtime
// directory, open to every local user, taking one request of type T
// per connection. The daemon decides by the peer's uid what it does
// with a request.
type Socket[T any] struct{ dmsocket.Endpoint[T] }

var (
	ApplySocket   = Socket[greeterconfig.Change]{dmsocket.Apply}
	CursorSocket  = Socket[cursorrecord.Cursor]{dmsocket.Endpoint[cursorrecord.Cursor]{Name: "cursor.sock", MaxRequest: 4096, Exchange: exchangeTimeout}}
	SplashSocket  = Socket[Presented]{dmsocket.Endpoint[Presented]{Name: "splash.sock", MaxRequest: 64, Exchange: exchangeTimeout}}
	UpgradeSocket = Socket[UpgradeRequest]{dmsocket.Endpoint[UpgradeRequest]{Name: "upgrade.sock", MaxRequest: 1 << 20, Exchange: upgradeExchange}}
	SwitchSocket  = Socket[SwitchRequest]{dmsocket.Endpoint[SwitchRequest]{Name: "switch.sock", MaxRequest: 64, Exchange: exchangeTimeout}}
)

type Presented struct{}

type SwitchRequest struct{}

const RuntimeDirEnv = "LYS_LOGIN_RUNTIME_DIR"

// exchangeTimeout bounds one exchange on most sockets.
const exchangeTimeout = 30 * time.Second

type peer struct {
	peercred.Credentials
	conn *net.UnixConn
}

func peerOf(conn *net.UnixConn) (peer, error) {
	cred, err := peercred.Of(conn)
	if err != nil {
		return peer{}, fmt.Errorf("who asks: %w", err)
	}
	return peer{Credentials: cred, conn: conn}, nil
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

// handler carries out a request from a peer within ctx and returns the
// file it went to, or the refusal the client sees.
type handler[T any] func(ctx context.Context, p peer, req T) (string, error)

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
				log.Printf("dm: %s: %v", s.Name, err)
			}
		}()
	}
}

func (s Socket[T]) answer(conn *net.UnixConn, handle handler[T]) error {
	deadline := time.Now().Add(s.Exchange)
	_ = conn.SetDeadline(deadline)
	p, err := peerOf(conn)
	if err != nil {
		return err
	}
	var req T
	if err := json.NewDecoder(io.LimitReader(conn, s.MaxRequest)).Decode(&req); err != nil {
		refusal := dmsocket.Refuse(dmsocket.Invalid, "malformed request: "+err.Error())
		return send(conn, dmsocket.Answer("", refusal), fmt.Errorf("decode uid %d's request: %w", p.UID, err))
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	path, err := handle(ctx, p, req)
	if err != nil {
		return send(conn, dmsocket.Answer("", err), fmt.Errorf("refused uid %d: %w", p.UID, err))
	}
	return send(conn, dmsocket.Answer(path, nil), nil)
}

// send writes r, returning cause, or the write's failure without one.
func send(conn net.Conn, r dmsocket.Reply, cause error) error {
	if err := json.NewEncoder(conn).Encode(r); err != nil && cause == nil {
		return fmt.Errorf("reply: %w", err)
	}
	return cause
}

func gated[T any](gate *sync.RWMutex, handle handler[T]) handler[T] {
	return func(ctx context.Context, p peer, req T) (string, error) {
		gate.RLock()
		defer gate.RUnlock()
		return handle(ctx, p, req)
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
	return func(_ context.Context, p peer, _ Presented) (string, error) {
		if p.UID != 0 && !isGreeter(p.UID) {
			return "", dmsocket.Refuse(dmsocket.Unauthorized, "only the greeter may take the screen from the splash")
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
	return func(_ context.Context, p peer, c cursorrecord.Cursor) (string, error) {
		if !owner.runs(p.UID) {
			return "", dmsocket.Refuse(dmsocket.Unauthorized, "only the user logged in here may record their session's cursor")
		}
		return cursorrecord.Write(dir, p.UID, c)
	}
}

const switchQueueWait = 5 * time.Second

func askSwitch(owner *sessionOwners, switches chan<- struct{}) handler[SwitchRequest] {
	return func(_ context.Context, p peer, _ SwitchRequest) (string, error) {
		if p.UID != 0 && !owner.runs(p.UID) {
			return "", dmsocket.Refuse(dmsocket.Unauthorized, "only root and the users logged in here may switch users")
		}
		select {
		case switches <- struct{}{}:
			log.Printf("dm: uid %d asked to switch users", p.UID)
			return "", nil
		case <-time.After(switchQueueWait):
			return "", fmt.Errorf("the daemon took no switch in %s", switchQueueWait)
		}
	}
}
