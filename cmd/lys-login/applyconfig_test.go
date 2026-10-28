package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// apply-config refuses what it cannot carry out before it reaches the
// display manager: a missing staged file, --config from a user, and a
// staged file without login screen keys.
func TestApplyConfigUsage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes the config itself")
	}
	staged := filepath.Join(t.TempDir(), "staged.yaml")
	if err := os.WriteFile(staged, []byte("general:\n  foo: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no staged file", nil, 2, "usage"},
		{"two staged files", []string{"a", "b"}, 2, "usage"},
		{"--config from a user", []string{"--config", "/etc/x/config.yaml", staged}, 2, "--config is root's"},
		{"no login keys", []string{staged}, 1, "no recognised login screen keys"},
	} {
		cmd := exec.Command(os.Args[0], append([]string{"apply-config"}, c.args...)...)
		cmd.Env = append(os.Environ(), "LYS_LOGIN_TEST_MAIN=1")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		_ = cmd.Run()
		if code := cmd.ProcessState.ExitCode(); code != c.code || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: exit %d, stderr %q; want %d and %q", c.name, code, stderr.String(), c.code, c.want)
		}
	}
}
