package fprint

import (
	"errors"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/intconv"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"
)

const hangupBound = 5 * time.Second

type sentPipe struct {
	read   int
	write  dbus.UnixFD
	closed bool
}

func newSentPipe(t *testing.T) *sentPipe {
	t.Helper()
	var fds [2]int
	if err := unix.Pipe2(fds[:], unix.O_CLOEXEC); err != nil {
		t.Fatalf("open a pipe: %v", err)
	}
	p := &sentPipe{read: fds[0], write: intconv.Must[dbus.UnixFD](fds[1])}
	t.Cleanup(func() {
		if !p.closed {
			_ = unix.Close(int(p.write))
		}
		_ = unix.Close(p.read)
	})
	return p
}

func (p *sentPipe) requireEveryCopyClosed(t *testing.T) {
	t.Helper()
	p.closed = true
	if err := unix.Close(int(p.write)); err != nil {
		t.Fatalf("close the write end %d: %v", p.write, err)
	}
	fds := []unix.PollFd{{Fd: intconv.Must[int32](p.read), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(hangupBound.Milliseconds()))
	for errors.Is(err, unix.EINTR) {
		n, err = unix.Poll(fds, int(hangupBound.Milliseconds()))
	}
	if err != nil || n != 1 || fds[0].Revents&unix.POLLHUP == 0 {
		t.Fatalf("a copy of descriptor %d the bus carried is still open %v later: poll = %d, %v, events %#x", p.write, hangupBound, n, err, fds[0].Revents)
	}
}

func TestVerifyClosesTheDescriptorsOfEveryStatusItGets(t *testing.T) {
	bus := dbustest.Start(t)
	r := newRigOn(t, bus, []string{"right-index-finger"}, false)
	r.verify(t)
	r.awaitStart(t)
	pipe := newSentPipe(t)
	impostor := bus.Conn(t)
	if err := impostor.Emit(devicePath, verifyStatusSignal.Name(), "verify-match", true, pipe.write); err != nil {
		t.Fatal(err)
	}
	if err := impostor.BusObject().CallWithContext(t.Context(), "org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Fatal(err)
	}
	if err := r.dev.conn.Emit("/net/reactivated/Fprint/Device/1", verifyStatusSignal.Name(), "verify-match", true, pipe.write); err != nil {
		t.Fatal(err)
	}
	if err := r.dev.conn.Emit(devicePath, verifyStatusSignal.Name(), "verify-no-match", true, pipe.write); err != nil {
		t.Fatal(err)
	}
	if got := r.awaitResult(t); got != NoMatch {
		t.Fatalf("result %v, want the reader's NoMatch: an impostor's or another device's match was taken", got)
	}
	r.awaitStart(t)
	r.cancel()
	if err := r.awaitEnd(t); err != nil {
		t.Fatal(err)
	}
	pipe.requireEveryCopyClosed(t)
}
