package lock

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-secrets/vault"
)

type fakeVault struct {
	mu       sync.Mutex
	state    vault.State
	statuses int
	unlocks  []string
	result   error
	block    chan struct{}
	finished int
}

func (f *fakeVault) Status() vault.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses++
	return vault.Status{State: f.state}
}

func (f *fakeVault) Unlock(ctx context.Context, password string) error {
	f.mu.Lock()
	f.unlocks = append(f.unlocks, password)
	block, result := f.block, f.result
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	f.finished++
	f.mu.Unlock()
	return result
}

func (f *fakeVault) calls() (statuses int, unlocks []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses, append([]string(nil), f.unlocks...)
}

func (*fakeVault) Items() ([]vault.Item, error) { return nil, vault.ErrLocked }

func (*fakeVault) Field(string, vault.Field, string) (vault.Value, error) {
	return vault.Value{}, vault.ErrLocked
}
func (*fakeVault) Login(context.Context, vault.LoginRequest) error          { return nil }
func (*fakeVault) SendLoginEmail(context.Context, vault.LoginRequest) error { return nil }
func (*fakeVault) LoginAPIKey(context.Context, vault.APIKeyRequest) error   { return nil }
func (*fakeVault) Lock()                                                    {}
func (*fakeVault) Logout() error                                            { return nil }
func (*fakeVault) Sync(context.Context) error                               { return nil }

func (*fakeVault) NetworkLogin(string, string) (vault.NetworkLogin, bool) {
	return vault.NetworkLogin{}, false
}

func serveVault(t *testing.T, f *fakeVault) {
	t.Helper()
	bus := dbustest.Session(t)
	release, err := vault.NewDaemon(t.Context(), f).Export(bus.Conn(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return buf
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func vaultHelper() []string { return []string{os.Args[0], "vault-unlock"} }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestVaultUnlockFiresOnceWhenEnabledAndLocked(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked}
	serveVault(t, f)
	v := newVaultUnlock(true, vaultHelper())
	pw := []byte("hunter2")
	v.start(pw)
	clear(pw)
	v.wait()
	eventually(t, "the vault unlock", func() bool { _, u := f.calls(); return len(u) > 0 })
	time.Sleep(100 * time.Millisecond)
	if _, unlocks := f.calls(); len(unlocks) != 1 || unlocks[0] != "hunter2" {
		t.Errorf("vault unlocks = %q, want the session password once", unlocks)
	}
}

func TestVaultUnlockSkippedWhenDisabled(t *testing.T) {
	logs := captureLog(t)
	v := newVaultUnlock(false, []string{filepath.Join(t.TempDir(), "missing")})
	v.start([]byte("hunter2"))
	v.wait()
	if got := logs.String(); got != "" {
		t.Errorf("disabled: log = %q, want no attempt", got)
	}
}

func TestVaultUnlockSkippedWhenNotLocked(t *testing.T) {
	for _, state := range []vault.State{vault.StateUnlocked, vault.StateLoggedOut} {
		f := &fakeVault{state: state}
		serveVault(t, f)
		if err := UnlockVault(strings.NewReader("hunter2")); err != nil {
			t.Errorf("state %d: %v", state, err)
		}
		if statuses, unlocks := f.calls(); statuses != 1 || len(unlocks) != 0 {
			t.Errorf("state %d: %d status calls, unlocks %q, want a status read and no unlock", state, statuses, unlocks)
		}
	}
}

func TestVaultUnlockSkippedWhenNotRunning(t *testing.T) {
	dbustest.Session(t)
	logs := captureLog(t)
	if err := UnlockVault(strings.NewReader("hunter2")); err != nil {
		t.Error(err)
	}
	if got := logs.String(); !strings.Contains(got, "lys-secrets is not running") {
		t.Errorf("log = %q, want the vault skipped as not running", got)
	}
}

func TestVaultUnlockWrongPasswordIsNotRetriedNorReported(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked, result: vault.ErrWrongPassword}
	serveVault(t, f)
	err := UnlockVault(strings.NewReader("hunter2"))
	if !errors.Is(err, vault.ErrWrongPassword) {
		t.Errorf("UnlockVault = %v, want the wrong password", err)
	}
	if err != nil && strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error %q carries the password", err)
	}
	if _, unlocks := f.calls(); len(unlocks) != 1 {
		t.Errorf("vault unlocks = %d, want exactly one attempt", len(unlocks))
	}
}

func TestVaultUnlockIsBounded(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked, block: make(chan struct{})}
	t.Cleanup(func() { close(f.block) })
	serveVault(t, f)
	begin := time.Now()
	if err := unlockVaultFrom(strings.NewReader("hunter2"), 200*time.Millisecond); err == nil {
		t.Error("a vault that never answers unlocked")
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("the attempt took %v, want it bounded by the timeout", d)
	}
}

func TestLockAfterUnlockHonouredWhileVaultUnlockInFlight(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked, block: make(chan struct{})}
	t.Cleanup(func() { close(f.block) })
	serveVault(t, f)
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	first := startLockProcess(t)
	if err := first.Wait(); err != nil {
		t.Fatalf("the unlocked lock process: %v", err)
	}
	eventually(t, "the vault unlock in flight", func() bool { _, u := f.calls(); return len(u) == 1 })
	second := startLockProcess(t)
	if err := second.Wait(); err != nil {
		t.Fatalf("a lock right after the unlock was not honoured: %v", err)
	}
	f.mu.Lock()
	finished := f.finished
	f.mu.Unlock()
	if finished != 0 {
		t.Error("the lock process outlived the vault attempt instead of handing it off")
	}
}

func startLockProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "lock-process")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(out); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func lockProcess() int {
	vu := newVaultUnlock(true, vaultHelper())
	defer vu.wait()
	release, err := holdSession(xdg.OS)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer release()
	vu.start([]byte("hunter2"))
	return 0
}
