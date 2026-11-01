package dm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStopGroupStopsTheCompositorBeforeItsClients(t *testing.T) {
	dir := t.TempDir()
	childPid, verdict := filepath.Join(dir, "child"), filepath.Join(dir, "verdict")
	script := `sleep 30 & echo $! > "$1"; trap 'if kill -0 "$(cat "$1")" 2>/dev/null; then echo alive; else echo gone; fi > "$2"; exit 0' TERM; wait`
	cmd := exec.Command("sh", "-c", script, "compositor", childPid, verdict)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var child int
	for deadline := time.Now().Add(5 * time.Second); child == 0; {
		if b, err := os.ReadFile(childPid); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		if time.Now().After(deadline) {
			t.Fatal("the stand-in compositor never started its client")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := stopGroup(cmd, exited); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(verdict); err != nil || strings.TrimSpace(string(b)) != "alive" {
		t.Fatalf("the client was %q when the compositor got SIGTERM (%v), want still alive", b, err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the compositor's client outlived the stopped group")
		}
	}
}
