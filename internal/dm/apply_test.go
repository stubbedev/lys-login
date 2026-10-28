package dm

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/internal/greeterconfig"
)

// serveApplyAt runs the apply socket for a greeter config in a scratch
// directory and returns the socket and the config's runtime.yaml.
func serveApplyAt(t *testing.T, owner *sessionOwner) (sock, runtime string) {
	t.Helper()
	sock = ApplySocket(sockdir.Dir(t))
	ln, err := listenApply(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("the apply socket is %v, %v; want every local user able to connect", fi, err)
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	go serveApply(ln, owner, cfg)
	return sock, filepath.Join(filepath.Dir(cfg), "runtime.yaml")
}

var colorChange = greeterconfig.Change{Staged: "login:\n  background-color: \"#ff0000\"\n"}

// The user whose session runs changes the login screen; nobody else
// does while the greeter is up or someone else is logged in.
func TestApplyIsTheLoggedInUsers(t *testing.T) {
	owner := newSessionOwner()
	sock, runtime := serveApplyAt(t, owner)
	me := uint32(os.Getuid())
	if me == 0 {
		t.Skip("root may always apply")
	}

	if _, err := SendChange(sock, colorChange); err == nil || !strings.Contains(err.Error(), "logged in") {
		t.Errorf("without a session: %v, want a refusal", err)
	}
	owner.set(me + 1)
	if _, err := SendChange(sock, colorChange); err == nil {
		t.Error("another user's session let this one apply")
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused change wrote runtime.yaml: %v", err)
	}

	owner.set(me)
	path, err := SendChange(sock, colorChange)
	if err != nil {
		t.Fatalf("the session's user: %v", err)
	}
	if data, _ := os.ReadFile(path); path != runtime || !strings.Contains(string(data), "#ff0000") {
		t.Errorf("wrote %s: %q", path, data)
	}

	owner.clear()
	if _, err := SendChange(sock, colorChange); err == nil {
		t.Error("the session's end left its user able to apply")
	}
}

func TestMayApply(t *testing.T) {
	owner := newSessionOwner()
	if !owner.mayApply(0) || owner.mayApply(1000) {
		t.Error("without a session only root may apply")
	}
	owner.set(1000)
	if !owner.mayApply(0) || !owner.mayApply(1000) || owner.mayApply(1001) {
		t.Error("with 1000's session root and 1000 may apply, 1001 not")
	}
}

func TestApplyRefusesAMalformedChange(t *testing.T) {
	owner := newSessionOwner()
	owner.set(uint32(os.Getuid()))
	sock, _ := serveApplyAt(t, owner)
	if _, err := SendChange(sock, greeterconfig.Change{Staged: "general:\n  foo: 1\n"}); err == nil || !strings.Contains(err.Error(), "no recognised login screen keys") {
		t.Errorf("no login keys: %v", err)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("not json"))
	_ = conn.(*net.UnixConn).CloseWrite()
	reply := make([]byte, 256)
	n, _ := conn.Read(reply)
	if !strings.Contains(string(reply[:n]), "malformed change") {
		t.Errorf("garbage = %q, want a refusal", reply[:n])
	}
}

func TestSendChangeWithoutADaemon(t *testing.T) {
	if _, err := SendChange(ApplySocket(sockdir.Dir(t)), colorChange); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("no daemon = %v, want ErrNoDaemon", err)
	}
}
