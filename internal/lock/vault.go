package lock

import (
	"bytes"
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-secrets/vault"
)

const (
	vaultUnlockTimeout = 10 * time.Second
	errNameHasNoOwner  = "org.freedesktop.DBus.Error.NameHasNoOwner"
)

type vaultUnlock struct {
	enabled bool
	timeout time.Duration
	wg      sync.WaitGroup
}

func newVaultUnlock(enabled bool) *vaultUnlock {
	return &vaultUnlock{enabled: enabled, timeout: vaultUnlockTimeout}
}

func (v *vaultUnlock) start(password []byte) {
	if !v.enabled {
		return
	}
	pw := bytes.Clone(password)
	v.wg.Go(func() {
		defer clear(pw)
		ctx, cancel := context.WithTimeout(context.Background(), v.timeout)
		defer cancel()
		if err := unlockVault(ctx, pw); err != nil {
			log.Printf("lock: vault: unlocking with the session password failed: %v", err)
		}
	})
}

func (v *vaultUnlock) wait() { v.wg.Wait() }

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
