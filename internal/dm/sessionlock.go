package dm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// sessionLockName is the file a session worker holds a lock on for as long as
// its session runs. The kernel drops the lock when the worker goes, however it
// goes, so a daemon that did not start the session can still see it is live.
const sessionLockName = "session.lock"

// holdSessionLock takes the lock in dir and records this process in it. The
// file stays open for the caller to close; it does not reach the session's
// processes, as Go opens it close-on-exec.
func holdSessionLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, sessionLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the daemon's own runtime directory
	if err != nil {
		return nil, fmt.Errorf("dm: open the session lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dm: another session holds %s: %w", path, err)
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dm: clear %s: %w", path, err)
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid())); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dm: write %s: %w", path, err)
	}
	return f, nil
}

// liveSession reports a session worker holding the lock in dir, and its pid.
func liveSession(dir string) (pid int, live bool, err error) {
	path := filepath.Join(dir, sessionLockName)
	f, err := os.Open(path) //nolint:gosec // the daemon's own runtime directory
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("dm: open the session lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); {
	case err == nil:
		return 0, false, nil
	case !errors.Is(err, syscall.EWOULDBLOCK):
		return 0, false, fmt.Errorf("dm: test %s: %w", path, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // the daemon's own runtime directory
	if err != nil {
		return 0, false, fmt.Errorf("dm: read %s: %w", path, err)
	}
	if pid, err = strconv.Atoi(strings.TrimSpace(string(data))); err != nil {
		return 0, true, fmt.Errorf("dm: %s names no worker: %w", path, err)
	}
	return pid, true, nil
}
