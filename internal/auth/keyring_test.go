package auth

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"
	"github.com/stubbedev/lys-secrets/control"
)

// fakeKeyring serves one control-socket exchange: it captures the
// request bytes and answers with the given result (and length).
func fakeKeyring(t *testing.T, length, result uint32) (path string, got <-chan []byte) {
	t.Helper()
	path = filepath.Join(sockdir.Dir(t), "control")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	ch := make(chan []byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		head := make([]byte, 13)
		if _, err := io.ReadFull(conn, head); err != nil {
			ch <- nil
			return
		}
		arg := make([]byte, binary.BigEndian.Uint32(head[9:13]))
		_, _ = io.ReadFull(conn, arg)
		ch <- append(head, arg...)
		var resp [8]byte
		binary.BigEndian.PutUint32(resp[0:4], length)
		binary.BigEndian.PutUint32(resp[4:8], result)
		_, _ = conn.Write(resp[:])
	}()
	return path, ch
}

// TestKeyringUnlockWire pins GKD_CONTROL_OP_UNLOCK: the credentials
// byte, then big-endian packet length, op, and the password.
func TestKeyringUnlockWire(t *testing.T) {
	path, got := fakeKeyring(t, 8, keyringResultOK)
	if err := keyringUnlock(path, []byte("hunter2")); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	msg := <-got
	want := []byte{0, 0, 0, 0, 19, 0, 0, 0, 1, 0, 0, 0, 7}
	want = append(want, "hunter2"...)
	if string(msg) != string(want) {
		t.Errorf("request = %v, want %v", msg, want)
	}
}

func TestKeyringUnlockFailures(t *testing.T) {
	for _, tc := range []struct {
		length, result uint32
		want           string
	}{
		{8, keyringDenied, "denied"},
		{8, keyringNoDaemon, "no daemon"},
		{8, 2, "daemon returned 2"},
		{12, keyringResultOK, "unexpected response length 12"},
	} {
		path, _ := fakeKeyring(t, tc.length, tc.result)
		err := keyringUnlock(path, []byte("pw"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("result %d/len %d: err = %v, want %q", tc.result, tc.length, err, tc.want)
		}
	}
	if err := keyringUnlock(filepath.Join(sockdir.Dir(t), "absent"), []byte("pw")); !errors.Is(err, errKeyringAbsent) {
		t.Errorf("a missing daemon socket = %v, want errKeyringAbsent", err)
	}
}

func TestKeyringSockets(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("GNOME_KEYRING_CONTROL", "/run/user/1000/keyring-x")
	secrets, gnome, err := keyringSockets()
	if err != nil || secrets != "/run/user/1000/lys-secrets/control" || gnome != "/run/user/1000/keyring-x/control" {
		t.Errorf("with GNOME_KEYRING_CONTROL: %q, %q, %v", secrets, gnome, err)
	}
	for _, unset := range []string{"", "keyring-x"} {
		t.Setenv("GNOME_KEYRING_CONTROL", unset)
		if _, gnome, err := keyringSockets(); err != nil || gnome != "/run/user/1000/keyring/control" {
			t.Errorf("GNOME_KEYRING_CONTROL=%q: %q, %v", unset, gnome, err)
		}
	}
	for _, bad := range []string{"", "run/user/1000"} {
		t.Setenv("XDG_RUNTIME_DIR", bad)
		if secrets, gnome, err := keyringSockets(); err == nil {
			t.Errorf("XDG_RUNTIME_DIR=%q: %q, %q, want an error", bad, secrets, gnome)
		}
	}
}

type secretsUnlocker struct{ got chan string }

func (u secretsUnlocker) UnlockLogin(pw []byte) error {
	u.got <- string(pw)
	if string(pw) != "pw" {
		return control.ErrWrongPassword
	}
	return nil
}

// lys-secrets, when it runs, gets the password and gnome-keyring is not
// asked; without it gnome-keyring is.
func TestLoginKeyringPrefersLysSecrets(t *testing.T) {
	secrets := filepath.Join(sockdir.Dir(t), "lys-secrets", "control")
	ln, err := control.Listen(secrets)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	u := secretsUnlocker{got: make(chan string, 2)}
	go control.Serve(ctx, ln, u)
	gnome, gnomeGot := fakeKeyring(t, 8, keyringResultOK)

	if err := unlockLoginKeyring(secrets, gnome, []byte("pw")); err != nil {
		t.Fatalf("unlock through lys-secrets: %v", err)
	}
	if got := <-u.got; got != "pw" {
		t.Errorf("lys-secrets got %q", got)
	}
	if err := unlockLoginKeyring(secrets, gnome, []byte("other")); err == nil || !strings.Contains(err.Error(), "lys-secrets") {
		t.Errorf("a wrong password = %v, want lys-secrets' refusal", err)
	}
	select {
	case msg := <-gnomeGot:
		t.Errorf("gnome-keyring was asked while lys-secrets runs: %v", msg)
	default:
	}

	absent := filepath.Join(sockdir.Dir(t), "absent")
	if err := unlockLoginKeyring(absent, gnome, []byte("pw")); err != nil {
		t.Fatalf("fall back to gnome-keyring: %v", err)
	}
	if msg := <-gnomeGot; !strings.HasSuffix(string(msg), "pw") {
		t.Errorf("gnome-keyring got %q", msg)
	}
	if err := unlockLoginKeyring(absent, filepath.Join(sockdir.Dir(t), "absent2"), []byte("pw")); !errors.Is(err, errNoKeyring) {
		t.Errorf("neither running = %v, want errNoKeyring", err)
	}
}

// serveSecrets runs lys-secrets' control server at the session's socket.
func serveSecrets(t *testing.T, u control.Unlocker) {
	t.Helper()
	path, err := control.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := control.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { control.Serve(ctx, ln, u); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// A login hands the password over before the session's keyring runs:
// AwaitLoginKeyring waits for lys-secrets to start, then unlocks it.
func TestAwaitLoginKeyringWaitsForLysSecrets(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", sockdir.Dir(t))
	t.Setenv("GNOME_KEYRING_CONTROL", "")
	u := secretsUnlocker{got: make(chan string, 1)}
	errs := make(chan error, 1)
	go func() { errs <- AwaitLoginKeyring(t.Context(), []byte("pw")) }()
	time.Sleep(3 * keyringPoll)
	select {
	case err := <-errs:
		t.Fatalf("returned before a keyring ran: %v", err)
	default:
	}
	serveSecrets(t, u)
	if err := <-errs; err != nil {
		t.Fatalf("AwaitLoginKeyring = %v", err)
	}
	if got := <-u.got; got != "pw" {
		t.Errorf("lys-secrets got %q", got)
	}
}

// A keyring that refuses the password ends the wait at once; none
// running ends it when the context does, saying so.
func TestAwaitLoginKeyringGivesUp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", sockdir.Dir(t))
	t.Setenv("GNOME_KEYRING_CONTROL", "")
	ctx, cancel := context.WithTimeout(t.Context(), 2*keyringPoll)
	defer cancel()
	if err := AwaitLoginKeyring(ctx, []byte("pw")); !errors.Is(err, errNoKeyring) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no keyring = %v, want errNoKeyring and the deadline", err)
	}

	serveSecrets(t, secretsUnlocker{got: make(chan string, 1)})
	if err := AwaitLoginKeyring(t.Context(), []byte("wrong")); !errors.Is(err, control.ErrWrongPassword) {
		t.Errorf("a wrong password = %v, want lys-secrets' refusal", err)
	}

	t.Setenv("XDG_RUNTIME_DIR", "")
	if err := AwaitLoginKeyring(t.Context(), []byte("pw")); err == nil || errors.Is(err, errNoKeyring) {
		t.Errorf("no runtime dir = %v, want that error", err)
	}
}
