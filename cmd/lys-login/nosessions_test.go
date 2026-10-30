package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoSessionsIsALoudError(t *testing.T) {
	empty := t.TempDir()
	greetArgs := []string{"--config", filepath.Join(empty, "none.yaml"), "--state", filepath.Join(empty, "last"), "--sessions", empty, "--xsessions", empty}
	for _, args := range [][]string{
		append([]string{"daemon", "--vt", "7", "--compositor", "/nonexistent/lys-wm", "--"}, greetArgs...),
		append([]string{"greet"}, greetArgs...),
	} {
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = append(os.Environ(), "LYS_LOGIN_TEST_MAIN=1")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		_ = cmd.Run()
		got := stderr.String()
		if code := cmd.ProcessState.ExitCode(); code != 2 ||
			!strings.Contains(got, "no sessions to log into") || !strings.Contains(got, empty) || !strings.Contains(got, "no `-- <argv>` fallback session") {
			t.Errorf("lys-login %s: exit %d, stderr %q; want 2 and the empty session dirs named", args[0], code, got)
		}
	}
}
