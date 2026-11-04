package dm

import (
	"os"
	"testing"
)

func TestTheSessionLockShowsALiveWorker(t *testing.T) {
	dir := t.TempDir()
	if _, live, err := liveSession(dir); live || err != nil {
		t.Fatalf("no lock file: live %v, %v", live, err)
	}
	f, err := holdSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	pid, live, err := liveSession(dir)
	if err != nil || !live || pid != os.Getpid() {
		t.Fatalf("held: pid %d, live %v, %v; want this process", pid, live, err)
	}
	if second, err := holdSessionLock(dir); err == nil {
		_ = second.Close()
		t.Error("a second session took the lock")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, live, err := liveSession(dir); live || err != nil {
		t.Errorf("released: live %v, %v; the file left behind must not count", live, err)
	}
}
