package dm

import (
	"errors"
	"io"
	"log"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFastFailuresBackOffThenEscalate(t *testing.T) {
	var n failedRuns
	var pauses []time.Duration
	for range 7 {
		n = n.after(2 * time.Second)
		pauses = append(pauses, n.pause())
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, escalatedPause, escalatedPause, escalatedPause}
	if !slices.Equal(pauses, want) {
		t.Errorf("pauses %v, want %v", pauses, want)
	}
	if !n.escalated() {
		t.Error("seven fast failures in a row did not escalate")
	}
	if n = n.after(fastFailure); n != 0 || n.pause() != retryPause || n.escalated() {
		t.Errorf("a greeter that ran %s before failing leaves %d failures and a %s pause, want a fresh start", fastFailure, n, n.pause())
	}
}

func TestTheFailureNoticeKeepsTheOutputFromDrivingTheConsole(t *testing.T) {
	got := failureNotice(escalateAfter, escalatedPause, errors.New("the greeter ended: exit status 1"), []string{"xwayland: \x1b[31m:0 is another user's\x1b[0m", "bad \xff byte\tand tab"})
	for _, want := range []string{"failed 5 times in a row", "again in 5m0s", "exit status 1", "  xwayland: ?[31m:0 is another user's?[0m\n", "  bad ? byte and tab\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("notice %q lacks %q", got, want)
		}
	}
	if strings.ContainsAny(got, "\x1b\t\r") {
		t.Errorf("notice %q carries control characters", got)
	}
}

type fakeClock struct {
	now   time.Time
	armed []time.Duration
}

func (c *fakeClock) clock() clock {
	return clock{
		now: func() time.Time { return c.now },
		after: func(d time.Duration) <-chan time.Time {
			c.armed = append(c.armed, d)
			return make(chan time.Time)
		},
	}
}

type said struct {
	vt   int
	text string
}

func backoffSeat(c *fakeClock, notices *[]said) *seat {
	d := testSeat(log.New(io.Discard, "", 0))
	d.c.clock = c.clock()
	d.c.say = func(vt int, text string) error { *notices = append(*notices, said{vt, text}); return nil }
	return d
}

func runGreeter(d *seat, c *fakeClock, vt int, ran time.Duration, r greetResult) {
	d.greeter = &greeterRun{vt: vt, started: c.now, cancel: func() {}}
	c.now = c.now.Add(ran)
	d.greeted(r)
}

func TestAGreeterThatKeepsFailingFastIsRetriedLessOftenAndExplainsItself(t *testing.T) {
	c := &fakeClock{now: time.Date(2026, 11, 13, 13, 19, 0, 0, time.UTC)}
	var notices []said
	d := backoffSeat(c, &notices)
	failed := greetResult{err: &greeterExit{err: errors.New("exit status 1"), output: []string{"xwayland: remove /tmp/.X0-lock: permission denied"}}}
	for range escalateAfter + 1 {
		runGreeter(d, c, d.o.VT, 2*time.Second, failed)
	}
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, escalatedPause, escalatedPause}
	if !slices.Equal(c.armed, want) {
		t.Errorf("armed %v, want %v", c.armed, want)
	}
	if len(notices) != 2 {
		t.Fatalf("notices %v, want one for each escalated failure", notices)
	}
	for _, n := range notices {
		if n.vt != d.o.VT || !strings.Contains(n.text, "/tmp/.X0-lock: permission denied") || !strings.Contains(n.text, "exit status 1") {
			t.Errorf("notice %+v, want the greeter's last output on VT %d", n, d.o.VT)
		}
	}
	runGreeter(d, c, d.o.VT, 5*time.Second, greetResult{})
	runGreeter(d, c, d.o.VT, 2*time.Second, failed)
	if got := c.armed[len(c.armed)-1]; got != retryPause {
		t.Errorf("after a greeter that ended cleanly the next failure waits %s, want %s", got, retryPause)
	}
	if len(notices) != 2 {
		t.Errorf("notices %v, want none for the first failure after a clean run", notices)
	}
}

func TestAGreeterThatFailsAfterRunningAWhileIsNoCrashLoop(t *testing.T) {
	c := &fakeClock{now: time.Date(2026, 11, 13, 13, 19, 0, 0, time.UTC)}
	var notices []said
	d := backoffSeat(c, &notices)
	failed := greetResult{err: &greeterExit{err: errors.New("exit status 1")}}
	for range escalateAfter * 2 {
		runGreeter(d, c, d.o.VT, time.Hour, failed)
	}
	for _, got := range c.armed {
		if got != retryPause {
			t.Fatalf("armed %v, want %s each time", c.armed, retryPause)
		}
	}
	if len(notices) != 0 {
		t.Errorf("notices %v, want none", notices)
	}
}

func TestAnEscalatedGreeterOnAnotherVTLeavesTheConsoleAlone(t *testing.T) {
	c := &fakeClock{now: time.Date(2026, 11, 13, 13, 19, 0, 0, time.UTC)}
	var notices []said
	d := backoffSeat(c, &notices)
	d.c.release = func(int) error { return nil }
	failed := greetResult{err: &greeterExit{err: errors.New("exit status 1")}}
	for range escalateAfter {
		runGreeter(d, c, d.o.VT+1, time.Second, failed)
	}
	if len(notices) != 0 {
		t.Errorf("notices %v, want none on a VT the greeter left", notices)
	}
}

func TestAStoppingDaemonArmsNoRetry(t *testing.T) {
	c := &fakeClock{now: time.Date(2026, 11, 13, 13, 19, 0, 0, time.UTC)}
	var notices []said
	d := backoffSeat(c, &notices)
	d.stopping = true
	runGreeter(d, c, d.o.VT, time.Second, greetResult{err: errors.New("context canceled")})
	if len(c.armed) != 0 || d.failures != 0 {
		t.Errorf("armed %v with %d failures, want nothing while stopping", c.armed, d.failures)
	}
}
