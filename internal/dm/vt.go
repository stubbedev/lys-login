package dm

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"golang.org/x/sys/unix"
)

const (
	vtOpenQuery   = 0x5600
	vtDisallocate = 0x5608
	logindTimeout = 5 * time.Second
	logindBus     = "org.freedesktop.login1"
	logindPath    = dbus.ObjectPath("/org/freedesktop/login1")
	logindManager = logindBus + ".Manager"
)

func freeVT() (int, io.Closer, error) {
	tty, err := os.OpenFile("/dev/tty0", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("open the console to find a free VT: %w", err)
	}
	defer func() { _ = tty.Close() }()
	vt, err := unix.IoctlGetInt(int(tty.Fd()), vtOpenQuery)
	if err != nil {
		return 0, nil, fmt.Errorf("find a free VT: %w", err)
	}
	if vt <= 0 {
		return 0, nil, fmt.Errorf("the console has no free VT: it answered %d", vt)
	}
	held, err := os.OpenFile(fmt.Sprintf("/dev/tty%d", vt), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("hold VT %d while its greeter starts: %w", vt, err)
	}
	return vt, held, nil
}

func disallocateVT(vt int) error {
	tty, err := os.OpenFile("/dev/tty0", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return fmt.Errorf("open the console to free VT %d: %w", vt, err)
	}
	defer func() { _ = tty.Close() }()
	if err := unix.IoctlSetInt(int(tty.Fd()), vtDisallocate, vt); err != nil {
		return fmt.Errorf("free VT %d: %w", vt, err)
	}
	return nil
}

func callLogind(method string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.Background(), logindTimeout)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("connect to logind for %s: %w", method, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Object(logindBus, logindPath).CallWithContext(ctx, logindManager+"."+method, 0, args...).Err; err != nil {
		return fmt.Errorf("logind %s%v: %w", method, args, err)
	}
	return nil
}

func lockLogindSession(id string) error { return callLogind("LockSession", id) }

func activateLogindSession(id string) error { return callLogind("ActivateSession", id) }
