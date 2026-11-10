package dm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/internal/cursorrecord"
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
		t.Fatalf("%s is %v, %v; want every local user able to connect", s.Name, fi, err)
	}
	go s.serve(ln, handle)
	return dir
}

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

func refusal(err error) (dmsocket.Refusal, bool) {
	refused, ok := errors.AsType[*dmsocket.RefusedError](err)
	if !ok {
		return 0, false
	}
	return refused.Refusal, true
}

func TestSendWithoutADaemon(t *testing.T) {
	if _, err := CursorSocket.Send(sockdir.Dir(t), cursorrecord.Cursor{Theme: "Bibata"}); !errors.Is(err, dmsocket.ErrNoDaemon) {
		t.Errorf("no daemon = %v, want ErrNoDaemon", err)
	}
}

func TestSocketsAnswerWithTheCallersCredentials(t *testing.T) {
	got := make(chan peer, 1)
	s := Socket[SwitchRequest]{dmsocket.Endpoint[SwitchRequest]{Name: "peer.sock", MaxRequest: 64, Exchange: exchangeTimeout}}
	dir := serveAt(t, s, func(_ context.Context, p peer, _ SwitchRequest) (string, error) {
		got <- p
		return "", nil
	})
	if _, err := s.Send(dir, SwitchRequest{}); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.uid != uint32(os.Getuid()) || p.pid != int32(os.Getpid()) {
		t.Errorf("peer = %+v, want uid %d pid %d", p, os.Getuid(), os.Getpid())
	}
}

func TestAMalformedRequestIsInvalid(t *testing.T) {
	s := Socket[SwitchRequest]{dmsocket.Endpoint[SwitchRequest]{Name: "small.sock", MaxRequest: 8, Exchange: exchangeTimeout}}
	var handled atomic.Int32
	dir := serveAt(t, s, func(context.Context, peer, SwitchRequest) (string, error) {
		handled.Add(1)
		return "", nil
	})
	raw := Socket[string]{dmsocket.Endpoint[string]{Name: s.Name, Exchange: exchangeTimeout}}
	for name, req := range map[string]string{"over the bound": strings.Repeat("x", 64), "not the request": "x"} {
		_, err := raw.Send(dir, req)
		if r, ok := refusal(err); !ok || r != dmsocket.Invalid || !strings.Contains(err.Error(), "malformed request") {
			t.Errorf("%s: %v, want an invalid request", name, err)
		}
	}
	if n := handled.Load(); n != 0 {
		t.Errorf("malformed requests reached the handler %d times", n)
	}
}

// The session that runs records its cursor under its uid, where the
// greeter reads it; nobody records one while the greeter is up or for
// someone else's session.
func TestCursorIsTheSessionsUsers(t *testing.T) {
	owner := newSessionOwners()
	records := filepath.Join(t.TempDir(), "cursor")
	dir := serveAt(t, CursorSocket, recordCursor(owner, records))
	me := me(t)
	bibata := cursorrecord.Cursor{Theme: "Bibata", Size: 32}

	if _, err := CursorSocket.Send(dir, bibata); err == nil || !strings.Contains(err.Error(), "logged in") {
		t.Errorf("without a session: %v, want a refusal", err)
	}
	owner.add(me + 1)
	if _, err := CursorSocket.Send(dir, bibata); err == nil {
		t.Error("another user's session let this one record a cursor")
	}
	if _, err := cursorrecord.Read(records, me); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a refused cursor was recorded: %v", err)
	}

	owner.add(me)
	if _, err := CursorSocket.Send(dir, bibata); err != nil {
		t.Fatalf("the session's user: %v", err)
	}
	if got, err := cursorrecord.Read(records, me); err != nil || got != bibata {
		t.Errorf("the record = %+v, %v; want %+v", got, err, bibata)
	}
}

func TestCursorRefusesAThemeThatIsNoDirectoryName(t *testing.T) {
	owner := newSessionOwners()
	me := me(t)
	owner.add(me)
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
