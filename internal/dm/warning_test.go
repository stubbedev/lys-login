package dm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func stderrFile(t *testing.T) (*os.File, string) {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	return f, fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

func TestWarningsCarryThePriorityOnlyOnTheJournal(t *testing.T) {
	f, stream := stderrFile(t)
	newWarningLogger(f, stream).Print("dm: locked")
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "<4>dm: locked\n" {
		t.Errorf("on the journal = %q, want the priority prefix first", data)
	}

	for _, other := range []string{"", "1:2"} {
		f, _ := stderrFile(t)
		newWarningLogger(f, other).Print("dm: locked")
		data, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "<4>") || !strings.Contains(string(data), "warning: dm: locked") {
			t.Errorf("JOURNAL_STREAM=%q: %q, want a plain warning", other, data)
		}
	}
}

func pipedLogin(t *testing.T) (*pendingLogin, *channel) {
	t.Helper()
	daemon, workerEnd := net.Pipe()
	t.Cleanup(func() { _ = daemon.Close(); _ = workerEnd.Close() })
	return &pendingLogin{w: standInWorker(t, daemon), cmd: []string{"session"}}, newChannel(workerEnd)
}

func runLogin(t *testing.T, login *pendingLogin, workerSide *channel, workerDoes func()) string {
	t.Helper()
	var warnings bytes.Buffer
	d := testSeat(log.New(&warnings, "", 0))
	go d.start(login, d.o.VT, nil)
	var start request
	if err := workerSide.recv(&start); err != nil {
		t.Fatal(err)
	}
	workerDoes()
	if err := workerSide.send(event{Kind: evEnded}); err != nil {
		t.Fatal(err)
	}
	for ev := range d.events {
		d.handle(ev)
		if len(d.running()) == 0 {
			break
		}
	}
	return warnings.String()
}

func TestDaemonLogsTheWorkersKeyringFailureAsItsOwnWarning(t *testing.T) {
	login, workerSide := pipedLogin(t)
	unlocked := make(chan error, 1)
	unlocked <- errors.New("denied")
	got := runLogin(t, login, workerSide, func() { reportKeyring(workerSide, "alice", unlocked) })
	if want := "dm: the login keyring of alice stays locked: denied\n"; got != want {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}

func TestDaemonWarnsOfNothingWhenTheKeyringUnlocks(t *testing.T) {
	login, workerSide := pipedLogin(t)
	unlocked := make(chan error, 1)
	unlocked <- nil
	got := runLogin(t, login, workerSide, func() { reportKeyring(workerSide, "alice", unlocked) })
	if got != "" {
		t.Errorf("warnings = %q, want none", got)
	}
}

func testSeat(warnings *log.Logger) *seat {
	quiet := func(int) error { return nil }
	return &seat{
		ctx: context.Background(), o: testOptions(), owners: newSessionOwners(), warnings: warnings,
		c:      console{activate: quiet, showText: func(int, bool) error { return nil }, stop: quiet},
		events: make(chan sessionEvent), switches: make(chan struct{}),
	}
}
