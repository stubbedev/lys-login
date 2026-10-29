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
	keyringResultOK   = 0
	keyringDenied     = 1
	keyringNoDaemon   = 3
	keyringHeaderSize = 8
)

// UnlockLoginKeyring hands the just-verified password to the keyring
// serving this session, so its login collection unlocks here: lys-secrets
// through its control socket when it runs, else a running gnome-keyring
// daemon. Under greetd autologin no password is typed at boot and the
// keyring starts locked: the lock screen is the first place the password
// appears. Best effort — failures are logged, never blocking or denying
// the unlock the user already earned.
func UnlockLoginKeyring(password []byte) {
	secrets, gnome, err := keyringSockets(xdg.OS)
	if err == nil {
		err = unlockLoginKeyring(secrets, gnome, password)
	}
	if err != nil {
		log.Printf("keyring: could not unlock the login keyring: %v", err)
	}
}

// keyringPoll spaces AwaitLoginKeyring's attempts.
const keyringPoll = 250 * time.Millisecond

// AwaitLoginKeyring is UnlockLoginKeyring for a session with
// environment env that is still starting: it tries until lys-secrets or
// gnome-keyring answers, and returns what that keyring said, or why
// none answered before ctx ended.
func AwaitLoginKeyring(ctx context.Context, env xdg.Env, password []byte) error {
	secrets, gnome, err := keyringSockets(env)
	if err != nil {
		return err
	}
	tick := time.NewTicker(keyringPoll)
	defer tick.Stop()
	for {
		err := unlockLoginKeyring(secrets, gnome, password)
		if !errors.Is(err, errNoKeyring) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", err, context.Cause(ctx))
		case <-tick.C:
		}
	}
}

// errNoKeyring is neither keyring running.
var errNoKeyring = errors.New("neither lys-secrets nor gnome-keyring is running")

// unlockLoginKeyring tries lys-secrets at secretsSock, then gnome-keyring
// at gnomeSock when lys-secrets is not running.
func unlockLoginKeyring(secretsSock, gnomeSock string, password []byte) error {
	err := control.Unlock(secretsSock, password)
	if err == nil {
		return nil
	}
	if !errors.Is(err, control.ErrNotRunning) {
		return fmt.Errorf("lys-secrets: %w", err)
	}
	if err := keyringUnlock(gnomeSock, password); err != nil {
		if errors.Is(err, errKeyringAbsent) {
			return fmt.Errorf("%w: %w", errNoKeyring, err)
		}
		return fmt.Errorf("lys-secrets is not running, and gnome-keyring: %w", err)
	}
	return nil
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

// errKeyringAbsent is no gnome-keyring daemon behind its socket.
var errKeyringAbsent = errors.New("gnome-keyring is not running")

// keyringUnlock speaks GKD_CONTROL_OP_UNLOCK directly, what
// pam_gnome_keyring.so does — not `gnome-keyring-daemon --unlock`,
// which starts a second daemon that steals the control socket from the
// real one. Wire format, big-endian u32s: one credentials byte (0; the
// daemon reads the peer's credentials off the socket), then
// [packet_len][op][arg_len][arg], answered by [8][result].
func keyringUnlock(path string, password []byte) error {
	conn, err := net.DialTimeout("unix", path, keyringTimeout)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("%w: %w", errKeyringAbsent, err)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(keyringTimeout))

	pwLen, err := intconv.Exact[uint32](len(password))
	if err != nil || pwLen > math.MaxUint32-12 {
		return errors.New("password too long")
	}
	msg := make([]byte, 0, 13+len(password))
	msg = append(msg, 0)
	msg = binary.BigEndian.AppendUint32(msg, 8+4+pwLen)
	msg = binary.BigEndian.AppendUint32(msg, keyringOpUnlock)
	msg = binary.BigEndian.AppendUint32(msg, pwLen)
	msg = append(msg, password...)
	_, err = conn.Write(msg)
	clear(msg)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	var resp [keyringHeaderSize]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
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
