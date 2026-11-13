package dm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	sharedTmp     = "/tmp"
	xSocketDir    = ".X11-unix"
	xLockMaxBytes = 32
	dirBatch      = 256
)

func xLockName(display int) string   { return ".X" + strconv.Itoa(display) + "-lock" }
func xSocketName(display int) string { return "X" + strconv.Itoa(display) }

func xLockDisplay(name string) (int, bool) {
	digits, ok := strings.CutPrefix(name, ".X")
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, "-lock"); !ok {
		return 0, false
	}
	return decimal(digits)
}

func decimal(s string) (int, bool) {
	if s == "" || len(s) > 9 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func xLockPID(content []byte) (int, bool) {
	pid, ok := decimal(strings.TrimSpace(string(content)))
	return pid, ok && pid > 0
}

func pidAlive(pid int) bool { return !errors.Is(unix.Kill(pid, 0), unix.ESRCH) }

func clearStaleX(tmp string, uid uint32, alive func(int) bool) ([]int, error) {
	dir, err := os.OpenFile(tmp, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0) //nolint:gosec // the daemon's fixed X directory root, opened as a directory without following links
	if err != nil {
		return nil, fmt.Errorf("open %s to clear the X displays of uid %d: %w", tmp, uid, err)
	}
	defer func() { _ = dir.Close() }()
	displays, err := xLockDisplays(dir)
	if err != nil {
		return nil, err
	}
	if len(displays) == 0 {
		return nil, nil
	}
	sockets, err := openSocketDir(dir)
	if err != nil {
		return nil, err
	}
	if sockets != nil {
		defer func() { _ = sockets.Close() }()
	}
	var cleared []int
	var errs []error
	for _, n := range displays {
		ok, err := clearDisplay(dir, sockets, n, uid, alive)
		if err != nil {
			errs = append(errs, fmt.Errorf("clear X display :%d of uid %d: %w", n, uid, err))
		}
		if ok {
			cleared = append(cleared, n)
		}
	}
	return cleared, errors.Join(errs...)
}

func xLockDisplays(dir *os.File) ([]int, error) {
	var displays []int
	for {
		names, err := dir.Readdirnames(dirBatch)
		for _, name := range names {
			if n, ok := xLockDisplay(name); ok {
				displays = append(displays, n)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dir.Name(), err)
		}
	}
	slices.Sort(displays)
	return displays, nil
}

func openSocketDir(tmp *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(tmp.Fd()), xSocketDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	path := filepath.Join(tmp.Name(), xSocketDir)
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func clearDisplay(tmp, sockets *os.File, n int, uid uint32, alive func(int) bool) (bool, error) {
	held, st, err := openOwned(tmp, xLockName(n), unix.S_IFREG, uid)
	if err != nil || held == nil {
		return false, err
	}
	defer func() { _ = held.Close() }()
	content, err := readHeld(held)
	if err != nil {
		return false, err
	}
	pid, ok := xLockPID(content)
	if len(content) > xLockMaxBytes || !ok || alive(pid) {
		return false, nil
	}
	var errs []error
	if sockets != nil {
		errs = append(errs, clearSocket(sockets, xSocketName(n), uid))
	}
	errs = append(errs, removeVerified(tmp, xLockName(n), st))
	if err := errors.Join(errs...); err != nil {
		return false, err
	}
	return true, nil
}

func clearSocket(dir *os.File, name string, uid uint32) error {
	sock, st, err := openOwned(dir, name, unix.S_IFSOCK, uid)
	if err != nil || sock == nil {
		return err
	}
	defer func() { _ = sock.Close() }()
	return removeVerified(dir, name, st)
}

func readHeld(held *os.File) ([]byte, error) {
	f, err := os.Open("/proc/self/fd/" + strconv.Itoa(int(held.Fd())))
	if err != nil {
		return nil, fmt.Errorf("reopen %s to read it: %w", held.Name(), err)
	}
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(io.LimitReader(f, xLockMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", held.Name(), err)
	}
	return content, nil
}

func openOwned(dir *os.File, name string, kind, uid uint32) (*os.File, unix.Stat_t, error) {
	path := filepath.Join(dir.Name(), name)
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, unix.Stat_t{}, nil
	case err != nil:
		return nil, unix.Stat_t{}, fmt.Errorf("open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != kind || st.Uid != uid {
		_ = f.Close()
		return nil, unix.Stat_t{}, nil
	}
	return f, st, nil
}

func removeVerified(dir *os.File, name string, want unix.Stat_t) error {
	dirFD := int(dir.Fd())
	aside := name + ".stale." + strconv.Itoa(os.Getpid())
	path := filepath.Join(dir.Name(), name)
	if err := unix.Renameat2(dirFD, name, dirFD, aside, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("move %s aside: %w", path, err)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(dirFD, aside, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("stat %s moved aside as %s: %w", path, aside, err)
	}
	if st.Dev != want.Dev || st.Ino != want.Ino {
		if err := unix.Renameat2(dirFD, aside, dirFD, name, unix.RENAME_NOREPLACE); err != nil {
			return fmt.Errorf("%s was replaced while it was cleared, and putting the new one back from %s failed: %w", path, aside, err)
		}
		return fmt.Errorf("%s was replaced while it was cleared: left in place", path)
	}
	if err := unix.Unlinkat(dirFD, aside, 0); err != nil {
		return fmt.Errorf("remove %s moved aside as %s: %w", path, aside, err)
	}
	return nil
}
