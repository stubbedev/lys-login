package lock

import (
	"errors"
	"os"
	"testing"

	"github.com/stubbedev/lys-lib/xdg"
)

func runtimeEnv(dir string) xdg.Env {
	return func(name string) string {
		if name == "XDG_RUNTIME_DIR" {
			return dir
		}
		return ""
	}
}

// One lys-login holds a session locked: a second finds it held until the
// first lets go.
func TestHoldSessionIsOnePerSession(t *testing.T) {
	env := runtimeEnv(t.TempDir())
	release, err := holdSession(env)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holdSession(env); !errors.Is(err, ErrHeld) {
		t.Fatalf("a second hold = %v, want ErrHeld", err)
	}
	release()
	again, err := holdSession(env)
	if err != nil {
		t.Fatalf("a hold after the release = %v", err)
	}
	again()
	if other, err := holdSession(runtimeEnv(t.TempDir())); err != nil {
		t.Errorf("another session's hold = %v", err)
	} else {
		other()
	}
}

// Without an absolute runtime dir nothing is held, nor written to a
// path relative to the working directory: every lock goes ahead.
func TestHoldSessionWithoutARuntimeDir(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, dir := range []string{"", "run/user/1000"} {
		first, err := holdSession(runtimeEnv(dir))
		if err != nil {
			t.Fatalf("XDG_RUNTIME_DIR=%q: %v", dir, err)
		}
		second, err := holdSession(runtimeEnv(dir))
		if err != nil {
			t.Fatalf("XDG_RUNTIME_DIR=%q: a second hold = %v", dir, err)
		}
		first()
		second()
	}
	if entries, err := os.ReadDir("."); err != nil || len(entries) != 0 {
		t.Errorf("the working directory holds %v, %v; want nothing", entries, err)
	}
}

// The screen reports the end of every lock: an unlock, and a refusal by
// the compositor.
func TestScreenReportsTheEnd(t *testing.T) {
	h := newHarness(t, nil)
	ends := 0
	h.s.d.OnEnded = func() { ends++ }
	h.s.Lock()
	h.s.ForceUnlock()
	if ends != 1 || !h.s.unlocked {
		t.Errorf("after an unlock: %d ends, unlocked %v", ends, h.s.unlocked)
	}
	h.s.Lock()
	h.locker.cfg.OnFinished()
	if ends != 2 || h.s.unlocked {
		t.Errorf("after the compositor ended it: %d ends, unlocked %v", ends, h.s.unlocked)
	}
}
