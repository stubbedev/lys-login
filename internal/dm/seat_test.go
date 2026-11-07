package dm

import (
	"io"
	"log"
	"net"
	"os"
	"slices"
	"testing"

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

func pipedSession(t *testing.T, d *seat, user string, vt int, id string) *channel {
	t.Helper()
	daemonEnd, workerEnd := net.Pipe()
	t.Cleanup(func() { _ = daemonEnd.Close(); _ = workerEnd.Close() })
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(1000 + vt)
	s := &session{w: &worker{proc: self, ch: newChannel(daemonEnd)}, cmd: []string{"session"}, uid: &uid, vt: vt, user: user, id: id}
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
