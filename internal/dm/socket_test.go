package dm

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/internal/cursorrecord"
	"github.com/stubbedev/lys-login/internal/greeterconfig"
)

// serveAt runs s with handle in a scratch runtime directory and
// returns that directory.
func serveAt[T any](t *testing.T, s Socket[T], handle handler[T]) string {
	t.Helper()
	dir := sockdir.Dir(t)
	ln, err := s.listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	if fi, err := os.Stat(s.Path(dir)); err != nil || fi.Mode().Perm() != 0o666 {
		t.Fatalf("%s is %v, %v; want every local user able to connect", s.name, fi, err)
	}
	go s.serve(ln, handle)
	return dir
}

// serveApplyAt runs the apply socket for a greeter config in a scratch
// directory and returns the runtime directory and the config's
// runtime.yaml.
func serveApplyAt(t *testing.T, owner *sessionOwner) (runtimeDir, runtime string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	return serveAt(t, ApplySocket, applyChange(owner, cfg)), filepath.Join(filepath.Dir(cfg), "runtime.yaml")
}

var colorChange = greeterconfig.Change{Staged: "login:\n  background-color: \"#ff0000\"\n"}

// me is the test's uid, which must not be root's: root may always
// apply.
func me(t *testing.T) uint32 {
	t.Helper()
	uid := uint32(os.Getuid())
	if uid == 0 {
		t.Skip("root may always apply")
	}
	return uid
}

// The user whose session runs changes the login screen; nobody else
// does while the greeter is up or someone else is logged in.
func TestApplyIsTheLoggedInUsers(t *testing.T) {
	owner := newSessionOwner()
	dir, runtime := serveApplyAt(t, owner)
	me := me(t)

	if _, err := ApplySocket.Send(dir, colorChange); err == nil || !strings.Contains(err.Error(), "logged in") {
		t.Errorf("without a session: %v, want a refusal", err)
	}
	owner.set(me + 1)
	if _, err := ApplySocket.Send(dir, colorChange); err == nil {
		t.Error("another user's session let this one apply")
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused change wrote runtime.yaml: %v", err)
	}

	owner.set(me)
	path, err := ApplySocket.Send(dir, colorChange)
	if err != nil {
		t.Fatalf("the session's user: %v", err)
	}
	if data, _ := os.ReadFile(path); path != runtime || !strings.Contains(string(data), "#ff0000") {
		t.Errorf("wrote %s: %q", path, data)
	}

	owner.clear()
	if _, err := ApplySocket.Send(dir, colorChange); err == nil {
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
	if owner.runs(0) || !owner.runs(1000) {
		t.Error("with 1000's session only 1000's session runs")
	}
}

func TestApplyRefusesAMalformedChange(t *testing.T) {
	owner := newSessionOwner()
	owner.set(uint32(os.Getuid()))
	dir, _ := serveApplyAt(t, owner)
	if _, err := ApplySocket.Send(dir, greeterconfig.Change{Staged: "general:\n  foo: 1\n"}); err == nil || !strings.Contains(err.Error(), "no recognised login screen keys") {
		t.Errorf("no login keys: %v", err)
	}
	conn, err := net.Dial("unix", ApplySocket.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("not json"))
	_ = conn.(*net.UnixConn).CloseWrite()
	reply := make([]byte, 256)
	n, _ := conn.Read(reply)
	if !strings.Contains(string(reply[:n]), "malformed request") {
		t.Errorf("garbage = %q, want a refusal", reply[:n])
	}
}

func TestSendWithoutADaemon(t *testing.T) {
	if _, err := ApplySocket.Send(sockdir.Dir(t), colorChange); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("no daemon = %v, want ErrNoDaemon", err)
	}
	if _, err := CursorSocket.Send(sockdir.Dir(t), cursorrecord.Cursor{Theme: "Bibata"}); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("no daemon = %v, want ErrNoDaemon", err)
	}
}

// The session that runs records its cursor under its uid, where the
// greeter reads it; nobody records one while the greeter is up or for
// someone else's session.
func TestCursorIsTheSessionsUsers(t *testing.T) {
	owner := newSessionOwner()
	records := filepath.Join(t.TempDir(), "cursor")
	dir := serveAt(t, CursorSocket, recordCursor(owner, records))
	me := me(t)
	bibata := cursorrecord.Cursor{Theme: "Bibata", Size: 32}

	if _, err := CursorSocket.Send(dir, bibata); err == nil || !strings.Contains(err.Error(), "logged in") {
		t.Errorf("without a session: %v, want a refusal", err)
	}
	owner.set(me + 1)
	if _, err := CursorSocket.Send(dir, bibata); err == nil {
		t.Error("another user's session let this one record a cursor")
	}
	if _, err := cursorrecord.Read(records, me); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused cursor was recorded: %v", err)
	}

	owner.set(me)
	if _, err := CursorSocket.Send(dir, bibata); err != nil {
		t.Fatalf("the session's user: %v", err)
	}
	if got, err := cursorrecord.Read(records, me); err != nil || got != bibata {
		t.Errorf("the record = %+v, %v; want %+v", got, err, bibata)
	}
}

func TestCursorRefusesAThemeThatIsNoDirectoryName(t *testing.T) {
	owner := newSessionOwner()
	me := me(t)
	owner.set(me)
	records := filepath.Join(t.TempDir(), "cursor")
	dir := serveAt(t, CursorSocket, recordCursor(owner, records))
	if _, err := CursorSocket.Send(dir, cursorrecord.Cursor{Theme: "../../etc", Size: 24}); err == nil || !strings.Contains(err.Error(), "malformed request") {
		t.Errorf("a theme outside the theme directories: %v, want a refusal", err)
	}
	if _, err := cursorrecord.Read(records, me); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused cursor was recorded: %v", err)
	}
}

func TestSplashHandOverIsTheGreeters(t *testing.T) {
	me := me(t)
	var quits atomic.Int32
	isGreeter := func(uid uint32) bool { return uid == me+1 }
	dir := serveAt(t, SplashSocket, handOverSplash(isGreeter, func() error { quits.Add(1); return nil }))
	if _, err := SplashSocket.Send(dir, Presented{}); err == nil || !strings.Contains(err.Error(), "only the greeter") {
		t.Errorf("another user: %v, want a refusal", err)
	}
	if n := quits.Load(); n != 0 {
		t.Fatalf("a refused hand-over quit the splash %d times", n)
	}

	dir = serveAt(t, SplashSocket, handOverSplash(func(uid uint32) bool { return uid == me }, func() error { quits.Add(1); return nil }))
	for range 2 {
		if _, err := SplashSocket.Send(dir, Presented{}); err != nil {
			t.Fatalf("the greeter: %v", err)
		}
	}
	if n := quits.Load(); n != 1 {
		t.Errorf("the splash was quit %d times, want once", n)
	}
}

func TestTakeScreenFromSplashReachesTheDaemon(t *testing.T) {
	me := me(t)
	quit := make(chan struct{}, 1)
	dir := serveAt(t, SplashSocket, handOverSplash(func(uid uint32) bool { return uid == me }, func() error { quit <- struct{}{}; return nil }))
	TakeScreenFromSplash(dir)()
	select {
	case <-quit:
	default:
		t.Fatal("the greeter's first frame did not quit the splash")
	}
	if TakeScreenFromSplash("") != nil {
		t.Error("without a display manager the greeter has a hand-over to make")
	}
	TakeScreenFromSplash(sockdir.Dir(t))()
}

func TestQuitSplashRetainsItsFrame(t *testing.T) {
	dir := t.TempDir()
	got := filepath.Join(dir, "args")
	splash := filepath.Join(dir, "lys-splash")
	if err := os.WriteFile(splash, []byte("#!/bin/sh\necho \"$@\" > "+got+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := quitSplash(splash)(); err != nil {
		t.Fatal(err)
	}
	if args, err := os.ReadFile(got); err != nil || string(args) != "quit --retain-splash\n" {
		t.Errorf("lys-splash ran with %q, %v; want quit --retain-splash", args, err)
	}
}

func TestQuitSplashWithoutSplash(t *testing.T) {
	if err := quitSplash("")(); err != nil {
		t.Errorf("no splash configured: %v", err)
	}
	if err := quitSplash(filepath.Join(t.TempDir(), "lys-splash"))(); err != nil {
		t.Errorf("lys-splash not installed: %v", err)
	}
	failing := filepath.Join(t.TempDir(), "lys-splash")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\necho no daemon >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := quitSplash(failing)(); err == nil || !strings.Contains(err.Error(), "no daemon") {
		t.Errorf("a failing lys-splash: %v, want its output", err)
	}
}
