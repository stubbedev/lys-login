package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// record-cursor records nothing for a session that names no cursor
// and refuses a theme that is no directory name, before it reaches
// the display manager.
func TestRecordCursor(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		env  []string
		code int
		want string
	}{
		{"an argument", []string{"x"}, nil, 2, "unexpected argument"},
		{"no cursor", nil, nil, 0, ""},
		{"a path for a theme", nil, []string{"XCURSOR_THEME=../../etc"}, 1, "not a directory name"},
	} {
		cmd := exec.Command(os.Args[0], append([]string{"record-cursor"}, c.args...)...)
		cmd.Env = append(os.Environ(), "LYS_LOGIN_TEST_MAIN=1", "XCURSOR_THEME=", "XCURSOR_SIZE=", "HYPRCURSOR_SIZE=")
		cmd.Env = append(cmd.Env, c.env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		_ = cmd.Run()
		if code := cmd.ProcessState.ExitCode(); code != c.code || !strings.Contains(stderr.String(), c.want) {
			t.Errorf("%s: exit %d, stderr %q; want %d and %q", c.name, code, stderr.String(), c.code, c.want)
		}
	}
}
