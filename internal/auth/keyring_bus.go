package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-secrets/vault"
)

const (
	keyringBusTimeout = 2 * time.Second

	busInterface = "org.freedesktop.DBus"

	secretServiceName = "org.freedesktop.secrets"
	gnomeKeyringName  = "org.gnome.keyring"

	errNameHasNoOwner      = "org.freedesktop.DBus.Error.NameHasNoOwner"
	secretServicePath      = dbus.ObjectPath("/org/freedesktop/secrets")
	secretServiceInterface = "org.freedesktop.Secret.Service"
	noObject               = dbus.ObjectPath("/")
	collectionInterface    = "org.freedesktop.Secret.Collection"
	startReplyStarted      = uint32(1)
	kwalletInterface       = "org.kde.KWallet"
	pamKwalletAdvice       = "KWallet takes a login password only from pam_kwallet in the PAM service"
	onlyProvidersKnown     = "lys-login hands the password only to lys-secrets and gnome-keyring"
)

var kwalletServices = []struct {
	name string
	path dbus.ObjectPath
}{
	{"org.kde.kwalletd6", "/modules/kwalletd6"},
	{"org.kde.kwalletd5", "/modules/kwalletd5"},
}

func sessionBusAddress(env xdg.Env) (string, error) {
	if address := env("DBUS_SESSION_BUS_ADDRESS"); address != "" {
		return address, nil
	}
	runtime, ok := env.RuntimeHome()
	if !ok {
		return "", errors.New("keyring: neither DBUS_SESSION_BUS_ADDRESS nor an absolute XDG_RUNTIME_DIR names the session bus")
	}
	return "unix:path=" + dbus.EscapeBusAddressValue(filepath.Join(runtime, "bus")), nil
}

type keyringBus struct {
	address    string
	conn       *dbus.Conn
	cancel     context.CancelFunc
	connectErr error
	activated  bool
	started    bool
	refused    []string
}

func (b *keyringBus) close() {
	if b.conn != nil {
		_ = b.conn.Close()
		b.cancel()
	}
}

func (b *keyringBus) connect() bool {
	if b.conn != nil {
		return true
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(keyringBusTimeout, cancel)
	defer timer.Stop()
	conn, err := dbus.Connect(b.address, dbus.WithContext(ctx))
	if err != nil {
		cancel()
		b.connectErr = err
		return false
	}
	b.conn, b.cancel, b.connectErr = conn, cancel, nil
	return true
}

func (b *keyringBus) activate(ctx context.Context) bool {
	if b.activated {
		return b.started
	}
	if !b.connect() {
		return false
	}
	b.activated = true
	b.started = b.startProvider(ctx)
	return b.started
}

func (b *keyringBus) startProvider(ctx context.Context) bool {
	for _, name := range []string{vault.ServiceName, secretServiceName, gnomeKeyringName} {
		started, running, err := b.startService(ctx, name)
		if started {
			return true
		}
		if err != nil {
			b.refused = append(b.refused, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if running && name != vault.ServiceName {
			return false
		}
	}
	return false
}

func (b *keyringBus) startService(ctx context.Context, name string) (started, running bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, keyringBusTimeout)
	defer cancel()
	var reply uint32
	err = b.conn.BusObject().CallWithContext(ctx, busInterface+".StartServiceByName", 0, name, uint32(0)).Store(&reply)
	if errors.Is(err, context.DeadlineExceeded) {
		return true, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return reply == startReplyStarted, true, nil
}

func (b *keyringBus) giveUp(ctx context.Context, absent error) error {
	if cause := context.Cause(ctx); cause != nil {
		absent = fmt.Errorf("%w: %w", absent, cause)
	}
	if b.conn == nil {
		return fmt.Errorf("%w; no session bus at %s: %w", absent, b.address, b.connectErr)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyringBusTimeout)
	defer cancel()
	found, err := b.findProvider(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("%w; asking the session bus who provides secrets: %w", absent, err)
	case found.unlocked:
		return nil
	case found.locked != "":
		return fmt.Errorf("%w; %s", absent, found.locked)
	}
	return fmt.Errorf("%w; no provider on the session bus: %s", absent, strings.Join(b.refused, "; "))
}

type providerFinding struct {
	unlocked bool
	locked   string
}

func (b *keyringBus) findProvider(ctx context.Context) (providerFinding, error) {
	owner, err := b.owner(ctx, secretServiceName)
	if err != nil {
		return providerFinding{}, err
	}
	if owner != "" {
		locked, err := b.defaultCollectionLocked(ctx, owner)
		who := b.describe(ctx, owner)
		if err != nil {
			return providerFinding{locked: fmt.Sprintf("%s is served by %s, whose default collection did not say whether it is locked (%v): %s", secretServiceName, who, err, onlyProvidersKnown)}, nil
		}
		if !locked {
			return providerFinding{unlocked: true}, nil
		}
		return providerFinding{locked: fmt.Sprintf("%s is served by %s, whose default collection is locked: %s; %s", secretServiceName, who, onlyProvidersKnown, pamKwalletAdvice)}, nil
	}
	for _, kwallet := range kwalletServices {
		owner, err := b.owner(ctx, kwallet.name)
		if err != nil {
			return providerFinding{}, err
		}
		if owner == "" {
			continue
		}
		open, err := b.kwalletOpen(ctx, owner, kwallet.path)
		if err != nil {
			return providerFinding{}, fmt.Errorf("%s: %w", kwallet.name, err)
		}
		if open {
			return providerFinding{unlocked: true}, nil
		}
		return providerFinding{locked: fmt.Sprintf("%s runs as %s with its wallet closed: %s", kwallet.name, b.describe(ctx, owner), pamKwalletAdvice)}, nil
	}
	var activatable []string
	if err := b.conn.BusObject().CallWithContext(ctx, busInterface+".ListActivatableNames", 0).Store(&activatable); err != nil {
		return providerFinding{}, fmt.Errorf("list activatable names: %w", err)
	}
	for _, kwallet := range kwalletServices {
		if slices.Contains(activatable, kwallet.name) {
			return providerFinding{locked: fmt.Sprintf("%s is installed but not running: %s", kwallet.name, pamKwalletAdvice)}, nil
		}
	}
	return providerFinding{}, nil
}

// defaultCollection reads the default alias, twice before calling it absent:
// gnome-keyring answers "/" for a real alias when the read beats the entry it
// schedules for a new client, and that entry exists by the next message.
func (b *keyringBus) defaultCollection(ctx context.Context, owner string) (dbus.ObjectPath, error) {
	service := b.conn.Object(owner, secretServicePath)
	var collection dbus.ObjectPath
	for range 2 {
		if err := service.CallWithContext(ctx, secretServiceInterface+".ReadAlias", dbus.FlagNoAutoStart, "default").Store(&collection); err != nil {
			return noObject, fmt.Errorf("read the default alias: %w", err)
		}
		if collection != noObject {
			return collection, nil
		}
	}
	return noObject, errors.New("no default alias")
}

func (b *keyringBus) defaultCollectionLocked(ctx context.Context, owner string) (bool, error) {
	collection, err := b.defaultCollection(ctx, owner)
	if err != nil {
		return false, err
	}
	var locked dbus.Variant
	if err := b.conn.Object(owner, collection).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", dbus.FlagNoAutoStart, collectionInterface, "Locked").Store(&locked); err != nil {
		return false, fmt.Errorf("read the Locked property of %s: %w", collection, err)
	}
	value, ok := locked.Value().(bool)
	if !ok {
		return false, fmt.Errorf("the Locked property of %s is %s, not a boolean", collection, locked.Signature())
	}
	return value, nil
}

func (b *keyringBus) owner(ctx context.Context, name string) (string, error) {
	owner, err := dbusx.NameOwner(ctx, b.conn, name)
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == errNameHasNoOwner {
		return "", nil
	}
	return owner, err
}

func (b *keyringBus) kwalletOpen(ctx context.Context, owner string, path dbus.ObjectPath) (bool, error) {
	obj := b.conn.Object(owner, path)
	var wallet string
	if err := obj.CallWithContext(ctx, kwalletInterface+".networkWallet", dbus.FlagNoAutoStart).Store(&wallet); err != nil {
		return false, fmt.Errorf("ask for the network wallet: %w", err)
	}
	var open bool
	if err := obj.CallWithContext(ctx, kwalletInterface+".isOpen", dbus.FlagNoAutoStart, wallet).Store(&open); err != nil {
		return false, fmt.Errorf("ask whether %s is open: %w", wallet, err)
	}
	return open, nil
}

func (b *keyringBus) describe(ctx context.Context, owner string) string {
	var pid uint32
	if err := b.conn.BusObject().CallWithContext(ctx, busInterface+".GetConnectionUnixProcessID", 0, owner).Store(&pid); err != nil {
		return owner
	}
	comm, err := os.ReadFile("/proc/" + strconv.FormatUint(uint64(pid), 10) + "/comm")
	if err != nil {
		return fmt.Sprintf("%s (pid %d)", owner, pid)
	}
	return fmt.Sprintf("%s (pid %d)", strings.TrimSpace(string(comm)), pid)
}
