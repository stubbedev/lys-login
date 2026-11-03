package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-login/internal/dm"
)

func runSelf(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "LYS_LOGIN_TEST_MAIN=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok {
		t.Fatalf("run %q: %v", args, err)
	}
	return exit.ExitCode(), string(out)
}

func TestUpgradeSaysWhenNoDaemonListens(t *testing.T) {
	short, err := os.MkdirTemp("", "lys-up-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	runtime := filepath.Join(short, "no-daemon")
	code, out := runSelf(t, "upgrade", "--", "--runtime-dir", runtime, "--", "--", "/bin/sh")
	if code != 1 || !strings.Contains(out, dm.ErrNoDaemon.Error()) || !strings.Contains(out, runtime) {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestUpgradeRefusesDaemonArgumentsThatWouldNotRun(t *testing.T) {
	empty := t.TempDir()
	code, out := runSelf(t, "upgrade", "--", "--", "--sessions", empty, "--xsessions", empty)
	if code != 2 || !strings.Contains(out, "nothing to log into") {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestDaemonResumeWithoutAStateChangesNothing(t *testing.T) {
	for _, args := range [][]string{
		{dm.ResumeMode, "--" + dm.ResumeCheckFlag, "--" + dm.ResumeStateFlag, "9", "--", "--", "--", "/bin/sh"},
		{dm.ResumeMode, "--" + dm.ResumeStateFlag, "9", "--", "--", "--", "/bin/sh"},
	} {
		code, out := runSelf(t, args...)
		if code != 1 || !strings.Contains(out, "handed-over state") {
			t.Errorf("%q: exit %d: %s", args, code, out)
		}
	}
}
