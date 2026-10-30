package lock

import (
	"bytes"
	"context"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-secrets/vault"
)

type fakeVault struct {
	mu       sync.Mutex
	state    vault.State
	statuses int
	unlocks  []string
	result   error
	block    chan struct{}
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

func runVaultUnlock(enabled bool, password string) {
	v := newVaultUnlock(enabled)
	pw := []byte(password)
	v.start(pw)
	clear(pw)
	v.wait()
}

func TestVaultUnlockFiresOnceWhenEnabledAndLocked(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked}
	serveVault(t, f)
	runVaultUnlock(true, "hunter2")
	if _, unlocks := f.calls(); len(unlocks) != 1 || unlocks[0] != "hunter2" {
		t.Errorf("vault unlocks = %q, want the session password once", unlocks)
	}
}

func TestVaultUnlockSkippedWhenDisabled(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked}
	serveVault(t, f)
	runVaultUnlock(false, "hunter2")
	if statuses, unlocks := f.calls(); statuses != 0 || len(unlocks) != 0 {
		t.Errorf("disabled: %d status calls, unlocks %q, want none", statuses, unlocks)
	}
}

func TestVaultUnlockSkippedWhenNotLocked(t *testing.T) {
	for _, state := range []vault.State{vault.StateUnlocked, vault.StateLoggedOut} {
		f := &fakeVault{state: state}
		serveVault(t, f)
		runVaultUnlock(true, "hunter2")
		if statuses, unlocks := f.calls(); statuses != 1 || len(unlocks) != 0 {
			t.Errorf("state %d: %d status calls, unlocks %q, want a status read and no unlock", state, statuses, unlocks)
		}
	}
}

func TestVaultUnlockSkippedWhenNotRunning(t *testing.T) {
	dbustest.Session(t)
	logs := captureLog(t)
	runVaultUnlock(true, "hunter2")
	if got := logs.String(); !strings.Contains(got, "lys-secrets is not running") {
		t.Errorf("log = %q, want the vault skipped as not running", got)
	}
}

func TestVaultUnlockWrongPasswordIsNotRetriedNorLogged(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked, result: vault.ErrWrongPassword}
	serveVault(t, f)
	logs := captureLog(t)
	runVaultUnlock(true, "hunter2")
	if _, unlocks := f.calls(); len(unlocks) != 1 {
		t.Errorf("vault unlocks = %d, want exactly one attempt", len(unlocks))
	}
	got := logs.String()
	if !strings.Contains(got, "unlocking with the session password failed") {
		t.Errorf("log = %q, want the failure logged", got)
	}
	if strings.Contains(got, "hunter2") {
		t.Errorf("log = %q carries the password", got)
	}
}

func TestVaultUnlockDoesNotBlockAndIsBounded(t *testing.T) {
	f := &fakeVault{state: vault.StateLocked, block: make(chan struct{})}
	t.Cleanup(func() { close(f.block) })
	serveVault(t, f)
	logs := captureLog(t)
	v := newVaultUnlock(true)
	v.timeout = 200 * time.Millisecond
	begin := time.Now()
	v.start([]byte("hunter2"))
	if d := time.Since(begin); d > 50*time.Millisecond {
		t.Errorf("start took %v, want it to return at once", d)
	}
	v.wait()
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("wait took %v, want it bounded by the timeout", d)
	}
	if got := logs.String(); !strings.Contains(got, "unlocking with the session password failed") {
		t.Errorf("log = %q, want the timeout logged", got)
	}
}
