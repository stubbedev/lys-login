package dm

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-login/internal/auth"
)

type consoleCalls struct {
	released []int
	switched []string
	locked   []string
}

func recordingSeat(calls *consoleCalls) *seat {
	d := testSeat(log.New(io.Discard, "", 0))
	d.c.release = func(vt int) error { calls.released = append(calls.released, vt); return nil }
	d.c.switchTo = func(id string) error { calls.switched = append(calls.switched, id); return nil }
	d.c.lock = func(id string) error { calls.locked = append(calls.locked, id); return nil }
	return d
}

func standInWorker(t *testing.T, daemonEnd net.Conn) *worker {
	t.Helper()
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := self.Release(); err != nil {
			t.Errorf("release the stand-in worker: %v", err)
		}
	})
	return &worker{proc: self, ch: newChannel(daemonEnd)}
}

func pipedSession(t *testing.T, d *seat, user string, vt int, id string) *channel {
	t.Helper()
	daemonEnd, workerEnd := net.Pipe()
	t.Cleanup(func() { _ = daemonEnd.Close(); _ = workerEnd.Close() })
	uid := uint32(1000 + vt)
	s := &session{w: standInWorker(t, daemonEnd), cmd: []string{"session"}, uid: &uid, vt: vt, user: user, id: id}
	d.adopt(s)
	return newChannel(workerEnd)
}

func TestTheLastSessionEndingReturnsToTheOneBeforeIt(t *testing.T) {
	var calls consoleCalls
	d := recordingSeat(&calls)
	pipedSession(t, d, "alice", d.o.VT, "1")
	bob := pipedSession(t, d, "bob", d.o.VT+1, "2")
	go func() { _ = bob.send(event{Kind: evEnded}) }()
	d.handle(<-d.events)
	if got := d.running(); len(got) != 1 || got[0].user != "alice" {
		t.Fatalf("running %v, want alice alone", got)
	}
	if !slices.Equal(calls.released, []int{d.o.VT + 1}) {
		t.Errorf("released VTs %v, want bob's", calls.released)
	}
	if !slices.Equal(calls.switched, []string{"1"}) {
		t.Errorf("switched to %v, want alice's session, not a greeter", calls.switched)
	}
	if !d.owners.runs(1000+uint32(d.o.VT)) || d.owners.runs(1000+uint32(d.o.VT)+1) {
		t.Error("the owners do not follow the sessions")
	}
}

func TestPickingAUserWithASessionSwitchesWithoutAuthenticating(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	r := testRequests()
	r.existing = func(user string) bool { return user == "alice" }
	logins := make(chan *pendingLogin, 1)
	go func() {
		l, err := testOptions().serveGreeter(server, r)
		if err != nil {
			t.Error(err)
		}
		logins <- l
	}()
	g := auth.NewGreetd(client, auth.Start{Session: func() auth.Session { return auth.Session{Cmd: []string{"niri"}} }})
	if err := g.Run("alice", func(p auth.Prompt) (string, bool) {
		t.Errorf("alice was asked %q though her session runs", p.Text)
		return "", false
	}); err != nil {
		t.Fatalf("login: %v", err)
	}
	l := <-logins
	if l.existing != "alice" || l.w != nil {
		t.Errorf("login %+v, want a switch to alice's session and no worker", l)
	}
}

func TestAutologinRunsOncePerBootWithoutAPassword(t *testing.T) {
	var calls consoleCalls
	d := recordingSeat(&calls)
	d.o.RuntimeDir = t.TempDir()
	d.o.Autologin = &Autologin{User: "alice", Session: auth.Session{Cmd: []string{"niri"}, Env: []string{auth.SessionTypeEnv + "=wayland"}}}
	if !d.autologin() {
		t.Fatal("the first autologin of the boot did not start")
	}
	running := d.running()
	if len(running) != 1 || running[0].user != "alice" || !slices.Equal(running[0].cmd, []string{"niri"}) {
		t.Fatalf("running %v, want alice's niri", running)
	}
	for len(d.running()) > 0 {
		d.handle(<-d.events)
	}
	if d.autologin() {
		t.Error("a second autologin in the same boot started; after a logout the greeter shows")
	}
}

type lines chan string

func (l lines) Write(p []byte) (int, error) {
	l <- string(p)
	return len(p), nil
}

func runGreeterSeat(t *testing.T, unseen <-chan time.Time) (*seat, lines) {
	t.Helper()
	warnings := make(lines, 8)
	d := testSeat(log.New(warnings, "", 0))
	ctx, stop := context.WithCancel(context.Background())
	d.ctx = ctx
	d.u = &upgrader{requests: make(chan *pendingUpgrade)}
	done := make(chan greetResult, 2)
	d.greeter = &greeterRun{vt: d.o.VT, done: done, unseen: unseen, cancel: func() {
		select {
		case done <- greetResult{err: context.Canceled}:
		default:
		}
	}}
	ran := make(chan struct{})
	go func() {
		d.run()
		close(ran)
	}()
	t.Cleanup(func() {
		stop()
		<-ran
	})
	return d, warnings
}

func TestAGreeterNeverOnScreenIsAWarning(t *testing.T) {
	expired := make(chan time.Time, 1)
	expired <- time.Now()
	_, warnings := runGreeterSeat(t, expired)
	select {
	case got := <-warnings:
		if want := fmt.Sprintf("dm: the greeter on VT 7 is not on screen %s after it started: its compositor presented none of its frames, so no login form shows\n", greeterShowWait); got != want {
			t.Errorf("warning %q, want %q", got, want)
		}
	case <-time.After(stepDeadline):
		t.Fatal("a greeter that never reached the screen was not reported")
	}
}

func TestAGreeterOnScreenIsNoWarning(t *testing.T) {
	me := me(t)
	unseen := make(chan time.Time, 1)
	d, warnings := runGreeterSeat(t, unseen)
	dir := serveAt(t, SplashSocket, greeterOnScreen(func(uid uint32) bool { return uid == me }, func() error { return nil }, d.greeterShown))
	if _, err := SplashSocket.Send(dir, Presented{}); err != nil {
		t.Fatalf("the greeter's first frame: %v", err)
	}
	unseen <- time.Now()
	if _, err := SplashSocket.Send(dir, Presented{}); err != nil {
		t.Fatalf("a later frame: %v", err)
	}
	select {
	case got := <-warnings:
		t.Errorf("a greeter on screen was reported: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
}
