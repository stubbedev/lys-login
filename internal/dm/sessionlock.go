package dm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

const sessionLockPattern = "session.*.lock"

func sessionLockPath(dir string, vt int) string {
	return filepath.Join(dir, "session."+strconv.Itoa(vt)+".lock")
}

func holdSessionLock(dir string, vt int) (*os.File, error) {
	path := sessionLockPath(dir, vt)
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

func liveSessions(dir string) ([]int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, sessionLockPattern))
	if err != nil {
		return nil, fmt.Errorf("dm: list the session locks in %s: %w", dir, err)
	}
	slices.Sort(paths)
	var pids []int
	var errs []error
	for _, path := range paths {
		pid, live, err := liveSessionAt(path)
		if err != nil {
			errs = append(errs, err)
		}
		if live && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids, errors.Join(errs...)
}

func liveSessionAt(path string) (pid int, live bool, err error) {
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
