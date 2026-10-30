package lock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-secrets/vault"
)

const (
	vaultUnlockTimeout = 10 * time.Second
	maxVaultPassword   = 4096
	errNameHasNoOwner  = "org.freedesktop.DBus.Error.NameHasNoOwner"
)

type vaultUnlock struct {
	helper []string
	wg     sync.WaitGroup
}

func newVaultUnlock(enabled bool, helper []string) *vaultUnlock {
	if !enabled {
		helper = nil
	}
	return &vaultUnlock{helper: helper}
}

func (v *vaultUnlock) start(password []byte) {
	if len(v.helper) == 0 {
		return
	}
	pw := bytes.Clone(password)
	v.wg.Go(func() {
		defer clear(pw)
		if err := spawnVaultUnlock(v.helper, pw); err != nil {
			log.Printf("lock: vault: unlocking with the session password failed: %v", err)
		}
	})
}

func (v *vaultUnlock) wait() { v.wg.Wait() }

func spawnVaultUnlock(argv []string, password []byte) error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // lys-login itself
	cmd.Stdin, cmd.Stderr = r, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = r.Close()
	if err != nil {
		return fmt.Errorf("start the vault helper: %w", err)
	}
	_ = cmd.Process.Release()
	if _, err := w.Write(password); err != nil {
		return fmt.Errorf("hand the password to the vault helper: %w", err)
	}
	return nil
}

func UnlockVault(r io.Reader) error {
	return unlockVaultFrom(r, vaultUnlockTimeout)
}

func unlockVaultFrom(r io.Reader, timeout time.Duration) error {
	buf := make([]byte, maxVaultPassword)
	defer clear(buf)
	n, err := io.ReadFull(r, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the password: %w", err)
	}
	if n == 0 || n == len(buf) {
		return errors.New("read the password: empty or too long")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := unlockVault(ctx, buf[:n]); err != nil {
		return fmt.Errorf("unlocking with the session password failed: %w", err)
	}
	return nil
}

func unlockVault(ctx context.Context, password []byte) error {
	conn, err := dbus.SessionBusPrivateNoAutoStartup(dbus.WithContext(ctx))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Auth(nil); err != nil {
		return err
	}
	if err := conn.Hello(); err != nil {
		return err
	}
	if _, err := dbusx.NameOwner(ctx, conn, vault.ServiceName); err != nil {
		if de, ok := errors.AsType[dbus.Error](err); ok && de.Name == errNameHasNoOwner {
			log.Printf("lock: vault: lys-secrets is not running; leaving the vault alone")
			return nil
		}
		return err
	}
	c := vault.NewClient(conn)
	st, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if st.State != vault.StateLocked {
		return nil
	}
	if err := c.Unlock(ctx, string(password)); err != nil {
		return err
	}
	log.Printf("lock: vault: unlocked with the session password")
	return nil
}
