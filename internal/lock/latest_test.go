package lock

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-lib/testbuild"
	"golang.org/x/sys/unix"
)

func TestABlockedLogindHoldsNeitherTheLockNorTheRelease(t *testing.T) {
	h := newHarness(t, func(l *config.LoginConfig) { l.Lock.GracePeriodMS = 0 })
	loop := testbuild.GoroutineID()
	var onLoop atomic.Int32
	var mu sync.Mutex
	var sent []bool
	entered, release := make(chan struct{}, 8), make(chan struct{})
	w := newLatest("logind", func(locked bool) {
		if testbuild.GoroutineID() == loop {
			onLoop.Add(1)
		}
		mu.Lock()
		sent = append(sent, locked)
		mu.Unlock()
		entered <- struct{}{}
		<-release
	})
	h.s.d.SetLockedHint = w.set
	h.lock(t)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the hint never left")
	}
	h.s.ForceUnlock()
	h.finishExit()
	if h.locker.active {
		t.Fatal("the release waited on logind")
	}
	close(release)
	w.drain(time.After(5 * time.Second))
	if got := onLoop.Load(); got != 0 {
		t.Errorf("%d hints on the loop's goroutine", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []bool{true, false}) {
		t.Errorf("hints sent = %v, want locked then unlocked in order", sent)
	}
}

func TestHintsCollapseToTheLatestAndDrainDelivers(t *testing.T) {
	var mu sync.Mutex
	var sent []bool
	entered, release := make(chan struct{}, 8), make(chan struct{})
	w := newLatest("logind", func(locked bool) {
		mu.Lock()
		sent = append(sent, locked)
		mu.Unlock()
		entered <- struct{}{}
		<-release
	})
	w.set(true)
	<-entered
	for range 10 {
		w.set(true)
		w.set(false)
	}
	close(release)
	w.drain(time.After(5 * time.Second))
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(sent, []bool{true, false}) {
		t.Errorf("hints sent = %v, want the one in flight and the latest", sent)
	}
}

func TestADrainGivesUpOnALogindThatNeverAnswers(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	w := newLatest("logind", func(bool) { <-release })
	w.set(true)
	timeout := make(chan time.Time)
	close(timeout)
	w.drain(timeout)
}

func TestAStdoutNobodyReadsLeavesTheLoopTicking(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := unix.FcntlInt(w.Fd(), unix.F_SETPIPE_SZ, 4096); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	loop := testbuild.GoroutineID()
	var onLoop atomic.Int32
	writing := make(chan struct{}, 1)
	line := newLatest("the locked line's reader", func(struct{}) {
		if testbuild.GoroutineID() == loop {
			onLoop.Add(1)
		}
		select {
		case writing <- struct{}{}:
		default:
		}
		_, _ = fmt.Fprintln(w, "locked")
	})
	h := newHarness(t, func(l *config.LoginConfig) { l.Lock.BlankTimeoutMS = 30000 })
	h.s.d.OnLocked = line.set
	h.lock(t)
	select {
	case <-writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the locked line was never written")
	}
	ticks := 0
	for range 100 {
		h.s.Activity()
		ticks++
	}
	h.locker.cfg.OnLocked()
	if ticks != 100 {
		t.Errorf("%d loop passes while stdout was full, want 100", ticks)
	}
	if got := onLoop.Load(); got != 0 {
		t.Errorf("%d writes to stdout on the loop's goroutine", got)
	}
	timeout := make(chan time.Time)
	close(timeout)
	line.drain(timeout)
}
