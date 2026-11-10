package polkitauth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/logind"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"

	"github.com/stubbedev/lys-login/internal/polkitauth/polkitauthtest"
)

const action Action = "dev.stubbe.test.action"

func TestStartTime(t *testing.T) {
	stat := []byte("4242 (a) b (c)) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 12345 67 18446744073709551615\n")
	if got, err := startTime(stat); err != nil || got != 987654 {
		t.Errorf("startTime = %d, %v; want 987654", got, err)
	}
	for name, bad := range map[string]string{
		"no command name": "4242 sh S 1",
		"truncated":       "4242 (sh) S 1 4242",
		"not a number":    "4242 (sh) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 soon 1",
	} {
		if _, err := startTime([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestProcessOfReadsTheKernelsStartTime(t *testing.T) {
	p, err := ProcessOf(int32(os.Getpid()), uint32(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	want, err := startTime(stat)
	if err != nil {
		t.Fatal(err)
	}
	if p.PID != uint32(os.Getpid()) || p.UID != uint32(os.Getuid()) || p.StartTime != want || want == 0 {
		t.Errorf("ProcessOf = %+v, want start time %d", p, want)
	}
	if _, err := ProcessOf(-1, 0); err == nil {
		t.Error("a process that does not exist has a start time")
	}
}

func systemBus(t *testing.T) *dbus.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), logind.CallTimeout)
	defer cancel()
	conn, err := logind.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestCheckAsksForTheProcessWithInteraction(t *testing.T) {
	fake := polkitauthtest.Start(t, polkitauthtest.Authorized)
	conn := systemBus(t)
	p := Process{PID: 4242, StartTime: 987654, UID: 1000}
	for _, c := range []struct {
		name   string
		answer polkitauthtest.Result
		want   Verdict
	}{
		{"authorized", polkitauthtest.Authorized, Authorized},
		{"denied", polkitauthtest.Denied, Denied},
		{"dismissed", polkitauthtest.Dismissed, Dismissed},
		{"no agent", polkitauthtest.Challenge, NoAgent},
	} {
		fake.Answer(c.answer)
		if got, err := Check(context.Background(), conn, action, p); err != nil || got != c.want {
			t.Errorf("%s: Check = %d, %v; want %d", c.name, got, err, c.want)
		}
	}
	checks := fake.Checks.Load()
	if len(checks) != 4 {
		t.Fatalf("polkit saw %d checks, want 4", len(checks))
	}
	ids := map[string]bool{}
	for _, c := range checks {
		d := c.Subject.Details
		if c.Subject.Kind != "unix-process" || d["pid"].Value() != uint32(4242) || d["start-time"].Value() != uint64(987654) || d["uid"].Value() != int32(1000) {
			t.Errorf("subject = %+v", c.Subject)
		}
		if c.Action != string(action) || c.Flags != allowUserInteraction || len(c.Details) != 0 {
			t.Errorf("check = %+v", c)
		}
		ids[c.CancellationID] = true
	}
	if len(ids) != len(checks) {
		t.Errorf("cancellation ids %v repeat", ids)
	}
	if n := len(fake.Cancelled.Load()); n != 0 {
		t.Errorf("answered checks were cancelled %d times", n)
	}
}

func TestCheckCancelsWhatItGivesUpOn(t *testing.T) {
	fake := polkitauthtest.StartHolding(t)
	conn := systemBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Check(ctx, conn, action, Process{PID: 1, StartTime: 1, UID: 1000}); err == nil {
		t.Fatal("a check polkit never answered succeeded")
	}
	checks, cancelled := fake.Checks.Load(), fake.Cancelled.Load()
	if len(checks) != 1 || len(cancelled) != 1 || cancelled[0] != checks[0].CancellationID {
		t.Errorf("checks %+v, cancelled %v; want the check cancelled", checks, cancelled)
	}
}

func TestCheckWithoutPolkit(t *testing.T) {
	dbustest.Start(t).UseAsSystemBus(t)
	conn := systemBus(t)
	if _, err := Check(context.Background(), conn, action, Process{PID: 1, StartTime: 1, UID: 1000}); err == nil {
		t.Error("a check without polkitd succeeded")
	}
}
