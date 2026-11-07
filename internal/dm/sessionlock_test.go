package dm

import (
	"os"
	"slices"
	"testing"
)

func TestTheSessionLocksShowEveryLiveWorker(t *testing.T) {
	dir := t.TempDir()
	if pids, err := liveSessions(dir); len(pids) != 0 || err != nil {
		t.Fatalf("no lock file: %v, %v", pids, err)
	}
	first, err := holdSessionLock(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := holdSessionLock(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if pids, err := liveSessions(dir); err != nil || !slices.Equal(pids, []int{os.Getpid(), os.Getpid()}) {
		t.Fatalf("two held: %v, %v; want this process twice", pids, err)
	}
	if again, err := holdSessionLock(dir, 1); err == nil {
		_ = again.Close()
		t.Error("a second session took VT 1's lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if pids, err := liveSessions(dir); err != nil || len(pids) != 1 {
		t.Errorf("one released: %v, %v; the file left behind must not count", pids, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if pids, err := liveSessions(dir); err != nil || len(pids) != 0 {
		t.Errorf("both released: %v, %v", pids, err)
	}
}
