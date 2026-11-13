package dm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/sockdir"
	"golang.org/x/sys/unix"
)

func TestXLockDisplay(t *testing.T) {
	for name, want := range map[string]int{".X0-lock": 0, ".X12-lock": 12} {
		if got, ok := xLockDisplay(name); !ok || got != want {
			t.Errorf("%s = %d %v, want display %d", name, got, ok, want)
		}
	}
	for _, name := range []string{".X-lock", ".X+1-lock", ".X01-lock", ".X1-lock.stale.7", "X1-lock", ".X1", ".X1a-lock", ".X9999999999-lock"} {
		if got, ok := xLockDisplay(name); ok {
			t.Errorf("%s = display %d, want none", name, got)
		}
	}
}

func TestXLockPID(t *testing.T) {
	if pid, ok := xLockPID([]byte(fmt.Sprintf("%10d\n", 4242))); !ok || pid != 4242 {
		t.Errorf("an X lock = %d %v, want 4242", pid, ok)
	}
	for _, content := range []string{"", "\n", "         0\n", "-5\n", "12 34\n", "pid\n"} {
		if pid, ok := xLockPID([]byte(content)); ok {
			t.Errorf("%q = pid %d, want none", content, pid)
		}
	}
}

type xTmp struct {
	t   *testing.T
	dir string
	uid uint32
}

func newXTmp(t *testing.T) xTmp {
	t.Helper()
	x := xTmp{t: t, dir: sockdir.Dir(t), uid: uint32(os.Getuid())}
	if err := os.Mkdir(filepath.Join(x.dir, xSocketDir), 0o777); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x xTmp) lock(n, pid int) {
	x.t.Helper()
	if err := os.WriteFile(filepath.Join(x.dir, xLockName(n)), fmt.Appendf(nil, "%10d\n", pid), 0o444); err != nil {
		x.t.Fatal(err)
	}
}

func (x xTmp) socket(n int) string {
	x.t.Helper()
	path := filepath.Join(x.dir, xSocketDir, xSocketName(n))
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		x.t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: path}); err != nil {
		x.t.Fatal(err)
	}
	return path
}

func (x xTmp) exists(rel string) bool {
	x.t.Helper()
	_, err := os.Lstat(filepath.Join(x.dir, rel))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		x.t.Fatal(err)
	}
	return err == nil
}

func (x xTmp) clear(uid uint32, live ...int) []int {
	x.t.Helper()
	cleared, err := clearStaleX(x.dir, uid, func(pid int) bool { return slices.Contains(live, pid) })
	if err != nil {
		x.t.Fatal(err)
	}
	return cleared
}

const deadPID, livePID = 4000001, 4000002

func TestAStaleXDisplayOfTheEndedSessionIsCleared(t *testing.T) {
	x := newXTmp(t)
	x.lock(0, deadPID)
	x.socket(0)
	if got := x.clear(x.uid); !slices.Equal(got, []int{0}) {
		t.Errorf("cleared %v, want :0", got)
	}
	if x.exists(xLockName(0)) || x.exists(filepath.Join(xSocketDir, xSocketName(0))) {
		t.Error("the stale lock or socket of :0 is still there")
	}
	entries, err := os.ReadDir(x.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != xSocketDir {
		t.Errorf("left %v, want only %s", entries, xSocketDir)
	}
}

func TestALiveXServerKeepsItsDisplay(t *testing.T) {
	x := newXTmp(t)
	x.lock(0, livePID)
	x.socket(0)
	if got := x.clear(x.uid, livePID); len(got) != 0 {
		t.Errorf("cleared %v, want nothing", got)
	}
	if !x.exists(xLockName(0)) || !x.exists(filepath.Join(xSocketDir, xSocketName(0))) {
		t.Error("a live server's display was cleared")
	}
}

func TestAnotherUsersXDisplayIsLeftAlone(t *testing.T) {
	x := newXTmp(t)
	x.lock(0, deadPID)
	x.socket(0)
	if got := x.clear(x.uid + 1); len(got) != 0 {
		t.Errorf("cleared %v, want nothing of another uid", got)
	}
	if !x.exists(xLockName(0)) || !x.exists(filepath.Join(xSocketDir, xSocketName(0))) {
		t.Error("another user's display was cleared")
	}
}

func TestAnXLockThatIsNoPlainFileIsLeftAlone(t *testing.T) {
	x := newXTmp(t)
	target := filepath.Join(x.dir, "target")
	if err := os.WriteFile(target, fmt.Appendf(nil, "%10d\n", deadPID), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(x.dir, xLockName(1))); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(x.dir, xLockName(2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x.dir, xLockName(3)), []byte("not a pid\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if got := x.clear(x.uid); len(got) != 0 {
		t.Errorf("cleared %v, want nothing", got)
	}
	for _, rel := range []string{"target", xLockName(1), xLockName(2), xLockName(3)} {
		if !x.exists(rel) {
			t.Errorf("%s was removed", rel)
		}
	}
}

func TestOnlyTheSocketOfAStaleDisplayGoes(t *testing.T) {
	x := newXTmp(t)
	x.lock(0, deadPID)
	x.socket(1)
	notSocket := filepath.Join(x.dir, xSocketDir, xSocketName(0))
	if err := os.WriteFile(notSocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := x.clear(x.uid); !slices.Equal(got, []int{0}) {
		t.Errorf("cleared %v, want :0", got)
	}
	if !x.exists(filepath.Join(xSocketDir, xSocketName(1))) {
		t.Error("the socket of :1, which has no stale lock, was removed")
	}
	if !x.exists(filepath.Join(xSocketDir, xSocketName(0))) {
		t.Error("a plain file where the socket of :0 would be was removed")
	}
}

func TestAStaleLockWithoutASocketDirectoryIsCleared(t *testing.T) {
	x := xTmp{t: t, dir: t.TempDir(), uid: uint32(os.Getuid())}
	x.lock(5, deadPID)
	if got := x.clear(x.uid); !slices.Equal(got, []int{5}) || x.exists(xLockName(5)) {
		t.Errorf("cleared %v, want :5 gone", got)
	}
}

func TestAnEntryReplacedWhileItIsClearedIsPutBack(t *testing.T) {
	x := newXTmp(t)
	x.lock(0, deadPID)
	other := filepath.Join(x.dir, "other")
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var want unix.Stat_t
	if err := unix.Lstat(other, &want); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(x.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	if err := removeVerified(dir, xLockName(0), want); err == nil {
		t.Error("removing an entry that is not the one checked succeeded")
	}
	if !x.exists(xLockName(0)) {
		t.Error("the entry that replaced the checked one was not put back")
	}
}
