package auth

import (
	"bytes"
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
	"github.com/stubbedev/lys-lib/xdg"
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
	if err := keyringRequest(path, keyringOpUnlock, []byte("hunter2")); err != nil {
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
		err := keyringRequest(path, keyringOpUnlock, []byte("pw"))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("result %d/len %d: err = %v, want %q", tc.result, tc.length, err, tc.want)
		}
	}
	if err := keyringRequest(filepath.Join(sockdir.Dir(t), "absent"), keyringOpUnlock, []byte("pw")); !errors.Is(err, errKeyringAbsent) {
		t.Errorf("a missing daemon socket = %v, want errKeyringAbsent", err)
	}
}

// sessionEnv is a session's environment holding vars, apart from the
// test's own.
func sessionEnv(vars map[string]string) xdg.Env {
	return func(name string) string { return vars[name] }
}

func TestKeyringSockets(t *testing.T) {
	vars := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "GNOME_KEYRING_CONTROL": "/run/user/1000/keyring-x"}
	secrets, gnome, err := keyringSockets(sessionEnv(vars))
	if err != nil || secrets != "/run/user/1000/lys-secrets/control" || gnome != "/run/user/1000/keyring-x/control" {
		t.Errorf("with GNOME_KEYRING_CONTROL: %q, %q, %v", secrets, gnome, err)
	}
	for _, unset := range []string{"", "keyring-x"} {
		vars["GNOME_KEYRING_CONTROL"] = unset
		if _, gnome, err := keyringSockets(sessionEnv(vars)); err != nil || gnome != "/run/user/1000/keyring/control" {
			t.Errorf("GNOME_KEYRING_CONTROL=%q: %q, %v", unset, gnome, err)
		}
	}
	for _, bad := range []string{"", "run/user/1000"} {
		vars["XDG_RUNTIME_DIR"] = bad
		if secrets, gnome, err := keyringSockets(sessionEnv(vars)); err == nil {
			t.Errorf("XDG_RUNTIME_DIR=%q: %q, %q, want an error", bad, secrets, gnome)
		}
	}
}

type secretsUnlocker struct{ got chan string }

func (u secretsUnlocker) RekeyLogin(old, next []byte) error {
	u.got <- "rekey " + string(old) + " -> " + string(next)
	if string(old) != "pw" {
		return control.ErrWrongPassword
	}
	return nil
}

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

	if err := unlockLoginKeyring(secrets, gnome, []byte("pw"), nil); err != nil {
		t.Fatalf("unlock through lys-secrets: %v", err)
	}
	if got := <-u.got; got != "pw" {
		t.Errorf("lys-secrets got %q", got)
	}
	if err := unlockLoginKeyring(secrets, gnome, []byte("other"), nil); err == nil || !strings.Contains(err.Error(), "lys-secrets") {
		t.Errorf("a wrong password = %v, want lys-secrets' refusal", err)
	}
	select {
	case msg := <-gnomeGot:
		t.Errorf("gnome-keyring was asked while lys-secrets runs: %v", msg)
	default:
	}

	absent := filepath.Join(sockdir.Dir(t), "absent")
	if err := unlockLoginKeyring(absent, gnome, []byte("pw"), nil); err != nil {
		t.Fatalf("fall back to gnome-keyring: %v", err)
	}
	if msg := <-gnomeGot; !strings.HasSuffix(string(msg), "pw") {
		t.Errorf("gnome-keyring got %q", msg)
	}
	if err := unlockLoginKeyring(absent, filepath.Join(sockdir.Dir(t), "absent2"), []byte("pw"), nil); !errors.Is(err, errNoKeyring) {
		t.Errorf("neither running = %v, want errNoKeyring", err)
	}
}

// serveSecrets runs lys-secrets' control server at the socket of the
// session with environment env.
func serveSecrets(t *testing.T, env xdg.Env, u control.Login) {
	t.Helper()
	path, err := control.SocketPath(env)
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
	env := sessionEnv(map[string]string{"XDG_RUNTIME_DIR": sockdir.Dir(t)})
	u := secretsUnlocker{got: make(chan string, 1)}
	errs := make(chan error, 1)
	go func() { errs <- AwaitLoginKeyring(t.Context(), env, []byte("pw"), nil) }()
	time.Sleep(3 * keyringPoll)
	select {
	case err := <-errs:
		t.Fatalf("returned before a keyring ran: %v", err)
	default:
	}
	serveSecrets(t, env, u)
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
	env := sessionEnv(map[string]string{"XDG_RUNTIME_DIR": sockdir.Dir(t)})
	ctx, cancel := context.WithTimeout(t.Context(), 2*keyringPoll)
	defer cancel()
	if err := AwaitLoginKeyring(ctx, env, []byte("pw"), nil); !errors.Is(err, errNoKeyring) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no keyring = %v, want errNoKeyring and the deadline", err)
	}

	serveSecrets(t, env, secretsUnlocker{got: make(chan string, 1)})
	if err := AwaitLoginKeyring(t.Context(), env, []byte("wrong"), nil); !errors.Is(err, control.ErrWrongPassword) {
		t.Errorf("a wrong password = %v, want lys-secrets' refusal", err)
	}

	if err := AwaitLoginKeyring(t.Context(), sessionEnv(nil), []byte("pw"), nil); err == nil || errors.Is(err, errNoKeyring) {
		t.Errorf("no runtime dir = %v, want that error", err)
	}
}

func TestAChangedPasswordMovesTheLoginCollectionThenUnlocksIt(t *testing.T) {
	secrets := filepath.Join(sockdir.Dir(t), "lys-secrets", "control")
	ln, err := control.Listen(secrets)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	u := secretsUnlocker{got: make(chan string, 4)}
	go control.Serve(ctx, ln, u)
	gnome := filepath.Join(sockdir.Dir(t), "absent")
	if err := unlockLoginKeyring(secrets, gnome, []byte("pw"), []byte("pw")); err != nil {
		t.Fatal(err)
	}
	if got := []string{<-u.got, <-u.got}; got[0] != "rekey pw -> pw" || got[1] != "pw" {
		t.Errorf("lys-secrets saw %q, want the rekey and then the unlock with the new password", got)
	}
	if err := unlockLoginKeyring(secrets, gnome, []byte("new"), []byte("wrong")); err == nil || !strings.Contains(err.Error(), "new password") {
		t.Errorf("a rekey from a password that never opened it: %v", err)
	}
}

func TestAChangedPasswordReachesGnomeKeyringAsAChange(t *testing.T) {
	path := filepath.Join(sockdir.Dir(t), "control")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ops := make(chan []byte, 2)
	go func() {
		for range 2 {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 256)
			n, _ := io.ReadAtLeast(conn, buf, 9)
			ops <- buf[:n]
			_, _ = conn.Write([]byte{0, 0, 0, 8, 0, 0, 0, 0})
			_ = conn.Close()
		}
	}()
	if err := gnomeKeyringUnlock(path, []byte("new"), []byte("old")); err != nil {
		t.Fatal(err)
	}
	change, unlock := <-ops, <-ops
	if op := binary.BigEndian.Uint32(change[5:9]); op != keyringOpChange || !bytes.Contains(change, []byte("old")) || !bytes.Contains(change, []byte("new")) {
		t.Errorf("the first request %q is not a change from old to new", change)
	}
	if op := binary.BigEndian.Uint32(unlock[5:9]); op != keyringOpUnlock {
		t.Errorf("the second request is op %d, want an unlock", op)
	}
}
