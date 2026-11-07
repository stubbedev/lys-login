package auth

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/intconv"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-secrets/control"
)

// keyringTimeout bounds the control-socket exchange: a locker must
// never hang on a wedged daemon.
const keyringTimeout = 5 * time.Second

// gnome-keyring control protocol constants (pam/gkr-pam-client.c).
const (
	keyringOpUnlock   = 1
	keyringOpChange   = 2
	keyringResultOK   = 0
	keyringDenied     = 1
	keyringNoDaemon   = 3
	keyringHeaderSize = 8
)

// unlockOrRekey hands the just-verified password to the keyring
// serving this session, so its login collection unlocks here: lys-secrets
// through its control socket when it runs, else gnome-keyring, started
// on the session bus when it is only activatable. Under greetd autologin
// no password is typed at boot and the keyring starts locked: the lock
// screen is the first place the password appears. Best effort, and
// bounded by unlockKeyringWait: failures are logged, never denying the
// unlock the user already earned.
// A previous password, the one a password change replaced, first moves the
// login collection to the new one.
func unlockOrRekey(password, previous []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), unlockKeyringWait)
	defer cancel()
	if err := awaitKeyring(ctx, xdg.OS, password, previous, waitForActivation); err != nil {
		log.Printf("keyring: could not unlock the login keyring: %v", err)
	}
}

// keyringPoll spaces the attempts of a wait for a keyring.
const keyringPoll = 250 * time.Millisecond

// AwaitLoginKeyring is unlockOrRekey for a session with
// environment env that is still starting: it activates a keyring on the
// session's bus and tries until lys-secrets or gnome-keyring answers,
// and returns what that keyring said, or what it found instead when
// none answered before ctx ended.
func AwaitLoginKeyring(ctx context.Context, env xdg.Env, password, previous []byte) error {
	return awaitKeyring(ctx, env, password, previous, waitForSession)
}

const unlockKeyringWait = 3 * time.Second

type keyringPatience int

const (
	waitForSession keyringPatience = iota
	waitForActivation
)

func awaitKeyring(ctx context.Context, env xdg.Env, password, previous []byte, patience keyringPatience) error {
	secrets, gnome, err := keyringSockets(env)
	if err != nil {
		return err
	}
	absent := unlockLoginKeyring(secrets, gnome, password, previous)
	if !errors.Is(absent, errNoKeyring) {
		return absent
	}
	address, err := sessionBusAddress(env)
	if err != nil {
		return err
	}
	bus := keyringBus{address: address}
	defer bus.close()
	tick := time.NewTicker(keyringPoll)
	defer tick.Stop()
	for {
		if !bus.activate(ctx) && patience == waitForActivation {
			return bus.giveUp(ctx, absent)
		}
		select {
		case <-ctx.Done():
			return bus.giveUp(ctx, absent)
		case <-tick.C:
		}
		absent = unlockLoginKeyring(secrets, gnome, password, previous)
		if !errors.Is(absent, errNoKeyring) {
			return absent
		}
	}
}

// errNoKeyring is neither keyring running.
var errNoKeyring = errors.New("neither lys-secrets nor gnome-keyring is running")

// unlockLoginKeyring tries lys-secrets at secretsSock, then gnome-keyring
// at gnomeSock when lys-secrets is not running.
func unlockLoginKeyring(secretsSock, gnomeSock string, password, previous []byte) error {
	err := lysSecretsUnlock(secretsSock, password, previous)
	if err == nil {
		return nil
	}
	if !errors.Is(err, control.ErrNotRunning) {
		return fmt.Errorf("lys-secrets: %w", err)
	}
	if err := gnomeKeyringUnlock(gnomeSock, password, previous); err != nil {
		if errors.Is(err, errKeyringAbsent) {
			return fmt.Errorf("%w: %w", errNoKeyring, err)
		}
		return fmt.Errorf("lys-secrets is not running, and gnome-keyring: %w", err)
	}
	return nil
}

func lysSecretsUnlock(sock string, password, previous []byte) error {
	if previous != nil {
		if err := control.Rekey(sock, previous, password); err != nil {
			return fmt.Errorf("move the login collection to the new password: %w", err)
		}
		log.Printf("keyring: lys-secrets moved the login collection to the new password")
	}
	return control.Unlock(sock, password)
}

func gnomeKeyringUnlock(sock string, password, previous []byte) error {
	if previous != nil {
		if err := keyringRequest(sock, keyringOpChange, previous, password); err != nil {
			return fmt.Errorf("move the login keyring to the new password: %w", err)
		}
		log.Printf("keyring: gnome-keyring moved the login keyring to the new password")
	}
	return keyringRequest(sock, keyringOpUnlock, password)
}

// keyringSockets are the lys-secrets control socket and gnome-keyring
// control socket of the session with environment env:
// $GNOME_KEYRING_CONTROL/control, else $XDG_RUNTIME_DIR/keyring/control.
func keyringSockets(env xdg.Env) (secrets, gnome string, err error) {
	secrets, err = control.SocketPath(env)
	if err != nil {
		return "", "", fmt.Errorf("keyring: %w", err)
	}
	gnomeDir := env("GNOME_KEYRING_CONTROL")
	if !filepath.IsAbs(gnomeDir) {
		runtime, ok := env.RuntimeHome()
		if !ok {
			return "", "", errors.New("keyring: XDG_RUNTIME_DIR is not set to an absolute path")
		}
		gnomeDir = filepath.Join(runtime, "keyring")
	}
	return secrets, filepath.Join(gnomeDir, "control"), nil
}

// errKeyringAbsent is no gnome-keyring daemon answering at its socket:
// none yet, or one its systemd socket unit is still starting.
var errKeyringAbsent = errors.New("gnome-keyring is not answering")

// keyringRequest speaks the GKD_CONTROL ops directly, what
// pam_gnome_keyring.so does — not `gnome-keyring-daemon --unlock`,
// which starts a second daemon that steals the control socket from the
// real one. Wire format, big-endian u32s: one credentials byte (0; the
// daemon reads the peer's credentials off the socket), then
// [packet_len][op][arg_len][arg], answered by [8][result].
func keyringRequest(path string, op uint32, args ...[]byte) error {
	conn, err := net.DialTimeout("unix", path, keyringTimeout)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %w", errKeyringAbsent, err)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(keyringTimeout))

	size := 8
	for _, arg := range args {
		size += 4 + len(arg)
	}
	packet, err := intconv.Exact[uint32](size)
	if err != nil || size > math.MaxUint32 {
		return errors.New("password too long")
	}
	msg := make([]byte, 0, 1+size)
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint32(msg, packet)
	msg = binary.BigEndian.AppendUint32(msg, op)
	for _, arg := range args {
		msg = binary.BigEndian.AppendUint32(msg, uint32(len(arg))) //nolint:gosec // bounded by size above
		msg = append(msg, arg...)
	}
	_, err = conn.Write(msg)
	clear(msg)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	var resp [keyringHeaderSize]byte
	if _, err := io.ReadFull(conn, resp[:]); errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: read: %w", errKeyringAbsent, err)
	} else if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if n := binary.BigEndian.Uint32(resp[0:4]); n != keyringHeaderSize {
		return fmt.Errorf("unexpected response length %d", n)
	}
	switch result := binary.BigEndian.Uint32(resp[4:8]); result {
	case keyringResultOK:
		return nil
	case keyringDenied:
		return errors.New("denied (keyring password differs from login password)")
	case keyringNoDaemon:
		return errors.New("no daemon")
	default:
		return fmt.Errorf("daemon returned %d", result)
	}
}
