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
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return &pendingLogin{w: &worker{proc: self, ch: newChannel(daemon)}, cmd: []string{"session"}}, newChannel(workerEnd)
}

func runLogin(t *testing.T, login *pendingLogin, workerSide *channel, workerDoes func()) string {
	t.Helper()
	var warnings bytes.Buffer
	errs := make(chan error, 1)
	go func() {
		errs <- login.run(context.Background(), func(uint32) {}, log.New(&warnings, "", 0), &upgrader{})
	}()
	var start request
	if err := workerSide.recv(&start); err != nil {
		t.Fatal(err)
	}
	workerDoes()
	if err := workerSide.send(event{Kind: evEnded}); err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
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
