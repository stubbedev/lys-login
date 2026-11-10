package dm

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/stubbedev/lys-lib/logind"
	"golang.org/x/sys/unix"
)

const (
	vtOpenQuery   = 0x5600
	vtDisallocate = 0x5608
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

func withLogind(what string, do func(context.Context, logind.Manager) error) error {
	dialing, cancel := context.WithTimeout(context.Background(), logind.CallTimeout)
	conn, err := logind.Dial(dialing)
	cancel()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = conn.Close() }()
	return do(context.Background(), logind.NewManager(conn))
}

func lockLogindSession(id string) error {
	return withLogind("lock session "+id, func(ctx context.Context, m logind.Manager) error {
		return m.LockSession(ctx, logind.SessionID(id))
	})
}

func activateLogindSession(id string) error {
	return withLogind("activate session "+id, func(ctx context.Context, m logind.Manager) error {
		return m.ActivateSession(ctx, logind.SessionID(id))
	})
}
