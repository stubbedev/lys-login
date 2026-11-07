package fprint

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

const devicePath = dbus.ObjectPath("/net/reactivated/Fprint/Device/0")

type fakeManager struct{ noDevice bool }

func (m *fakeManager) GetDefaultDevice() (dbus.ObjectPath, *dbus.Error) {
	if m.noDevice {
		return "", dbus.NewError(errNoDevice, []any{"No devices available"})
	}
	return devicePath, nil
}

type fakeDevice struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	fingers []string
	calls   []string
	started chan struct{}
}

func (d *fakeDevice) record(call string) {
	d.mu.Lock()
	d.calls = append(d.calls, call)
	d.mu.Unlock()
}

func (d *fakeDevice) recorded() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *fakeDevice) ListEnrolledFingers(user string) ([]string, *dbus.Error) {
	d.record("list " + user)
	if len(d.fingers) == 0 {
		return nil, dbus.NewError(errNoPrints, []any{"No enrolled prints"})
	}
	return d.fingers, nil
}

func (d *fakeDevice) Claim(user string) *dbus.Error { d.record("claim " + user); return nil }
func (d *fakeDevice) Release() *dbus.Error          { d.record("release"); return nil }
func (d *fakeDevice) VerifyStop() *dbus.Error       { d.record("stop"); return nil }

func (d *fakeDevice) VerifyStart(finger string) *dbus.Error {
	d.record("start " + finger)
	d.started <- struct{}{}
	return nil
}

func (d *fakeDevice) emit(t *testing.T, result string, done bool) {
	t.Helper()
	if err := d.conn.Emit(devicePath, DeviceIface+"."+statusMember, result, done); err != nil {
		t.Fatal(err)
	}
}

type rig struct {
	dev     *fakeDevice
	client  *dbus.Conn
	results chan Result
	ended   chan error
	cancel  context.CancelFunc
}

func newRig(t *testing.T, fingers []string, noDevice bool) *rig {
	t.Helper()
	bus := dbustest.Start(t)
	server := bus.Conn(t)
	dev := &fakeDevice{conn: server, fingers: fingers, started: make(chan struct{}, 8)}
	if err := server.Export(&fakeManager{noDevice: noDevice}, ManagerPath, ManagerIface); err != nil {
		t.Fatal(err)
	}
	if err := server.Export(dev, devicePath, DeviceIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := server.RequestName(BusName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own %s: %v %v", BusName, reply, err)
	}
	return &rig{dev: dev, client: bus.Conn(t), results: make(chan Result, 8), ended: make(chan error, 1)}
}

func (r *rig) verify(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(cancel)
	go func() { r.ended <- Verify(ctx, r.client, "ada", func(res Result) { r.results <- res }) }()
}

func (r *rig) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-r.dev.started:
	case err := <-r.ended:
		t.Fatalf("Verify ended before VerifyStart: %v; calls %v", err, r.dev.recorded())
	case <-time.After(10 * time.Second):
		t.Fatalf("no VerifyStart; calls %v", r.dev.recorded())
	}
}

func (r *rig) awaitResult(t *testing.T) Result {
	t.Helper()
	select {
	case res := <-r.results:
		return res
	case <-time.After(10 * time.Second):
		t.Fatalf("no result; calls %v", r.dev.recorded())
		return 0
	}
}

func (r *rig) awaitEnd(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.ended:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("Verify did not end; calls %v", r.dev.recorded())
		return nil
	}
}

func TestAMatchEndsTheVerificationAndReleasesTheReader(t *testing.T) {
	r := newRig(t, []string{"right-index-finger"}, false)
	r.verify(t)
	r.awaitStart(t)
	r.dev.emit(t, "verify-match", true)
	if got := r.awaitResult(t); got != Match {
		t.Fatalf("result %v, want Match", got)
	}
	if err := r.awaitEnd(t); err != nil {
		t.Fatal(err)
	}
	want := []string{"list ada", "claim ada", "start any", "stop", "release"}
	if got := r.dev.recorded(); !slices.Equal(got, want) {
		t.Errorf("calls %v, want %v", got, want)
	}
}

func TestANoMatchReportsAndScansAgain(t *testing.T) {
	r := newRig(t, []string{"right-index-finger"}, false)
	r.verify(t)
	r.awaitStart(t)
	r.dev.emit(t, "verify-retry-scan", false)
	if got := r.awaitResult(t); got != Retry {
		t.Fatalf("result %v, want Retry", got)
	}
	r.dev.emit(t, "verify-no-match", true)
	if got := r.awaitResult(t); got != NoMatch {
		t.Fatalf("result %v, want NoMatch", got)
	}
	r.awaitStart(t)
	r.cancel()
	if err := r.awaitEnd(t); err != nil {
		t.Fatal(err)
	}
	want := []string{"list ada", "claim ada", "start any", "stop", "start any", "stop", "release"}
	if got := r.dev.recorded(); !slices.Equal(got, want) {
		t.Errorf("calls %v, want %v", got, want)
	}
}

func TestAnotherDevicesStatusIsNotOurs(t *testing.T) {
	r := newRig(t, []string{"right-index-finger"}, false)
	r.verify(t)
	r.awaitStart(t)
	if err := r.dev.conn.Emit("/net/reactivated/Fprint/Device/1", DeviceIface+"."+statusMember, "verify-match", true); err != nil {
		t.Fatal(err)
	}
	r.dev.emit(t, "verify-no-match", true)
	if got := r.awaitResult(t); got != NoMatch {
		t.Fatalf("result %v, want NoMatch: another device's match was taken", got)
	}
}

func TestADisconnectedReaderEndsWithAnError(t *testing.T) {
	r := newRig(t, []string{"right-index-finger"}, false)
	r.verify(t)
	r.awaitStart(t)
	r.dev.emit(t, "verify-disconnected", true)
	if err := r.awaitEnd(t); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("a disconnected reader ended with %v", err)
	}
	if got := r.dev.recorded(); got[len(got)-1] != "release" {
		t.Errorf("the reader was not released: %v", got)
	}
}

func TestNothingToVerifyWithIsUnavailable(t *testing.T) {
	for name, r := range map[string]*rig{
		"no enrolled finger": newRig(t, nil, false),
		"no reader":          newRig(t, nil, true),
	} {
		r.verify(t)
		if err := r.awaitEnd(t); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want ErrUnavailable", name, err)
		}
		if slices.Contains(r.dev.recorded(), "claim ada") {
			t.Errorf("%s: the reader was claimed", name)
		}
	}
}

func TestNoFprintdIsUnavailable(t *testing.T) {
	bus := dbustest.Start(t)
	err := Verify(context.Background(), bus.Conn(t), "ada", func(Result) { t.Error("a result without fprintd") })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify without fprintd: %v", err)
	}
}
