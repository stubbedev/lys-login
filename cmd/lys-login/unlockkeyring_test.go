package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"
	"github.com/stubbedev/lys-secrets/control"
)

type recordingUnlocker struct{ got chan string }

func (u recordingUnlocker) UnlockLogin(pw []byte) error {
	u.got <- string(pw)
	if string(pw) != "pw" {
		return control.ErrWrongPassword
	}
	return nil
}

// runUnlockKeyring runs `lys-login unlock-keyring args...` in a session
// whose runtime dir is runtimeDir, with stdin on its standard input.
func runUnlockKeyring(t *testing.T, runtimeDir, stdin string, args ...string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"unlock-keyring"}, args...)...)
	cmd.Env = append(os.Environ(), "LYS_LOGIN_TEST_MAIN=1", "XDG_RUNTIME_DIR="+runtimeDir, "GNOME_KEYRING_CONTROL=")
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd, &stderr
}

func exitCode(t *testing.T, cmd *exec.Cmd, stderr *strings.Builder) int {
	t.Helper()
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
		return cmd.ProcessState.ExitCode()
	case <-time.After(20 * time.Second):
		t.Fatalf("lys-login unlock-keyring never exited; stderr:\n%s", stderr)
		return -1
	}
}

// serveControl serves lys-secrets' control socket under runtimeDir.
func serveControl(t *testing.T, runtimeDir string, u control.Unlocker) {
	t.Helper()
	ln, err := control.Listen(runtimeDir + "/lys-secrets/control")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { control.Serve(ctx, ln, u); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

// The helper a session worker starts waits for lys-secrets to come up in
// the new session and unlocks it with the password on its stdin.
func TestUnlockKeyringWaitsForTheSessionsKeyring(t *testing.T) {
	dir := sockdir.Dir(t)
	cmd, stderr := runUnlockKeyring(t, dir, "pw")
	time.Sleep(time.Second)
	u := recordingUnlocker{got: make(chan string, 1)}
	serveControl(t, dir, u)
	if code := exitCode(t, cmd, stderr); code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got := <-u.got; got != "pw" {
		t.Errorf("lys-secrets got %q", got)
	}
}

func TestUnlockKeyringFailures(t *testing.T) {
	dir := sockdir.Dir(t)
	serveControl(t, dir, recordingUnlocker{got: make(chan string, 1)})
	for _, c := range []struct {
		name, stdin string
		args        []string
		want        int
	}{
		{"a wrong password", "wrong", nil, 1},
		{"no password", "", nil, 1},
		{"an argument", "pw", []string{"extra"}, 2},
	} {
		cmd, stderr := runUnlockKeyring(t, dir, c.stdin, c.args...)
		if code := exitCode(t, cmd, stderr); code != c.want {
			t.Errorf("%s: exit %d, want %d; stderr:\n%s", c.name, code, c.want, stderr)
		}
	}
}
