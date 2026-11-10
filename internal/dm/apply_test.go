package dm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/peercred"
	"github.com/stubbedev/lys-lib/polkit"
	"github.com/stubbedev/lys-lib/polkit/polkittest"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
)

var colorChange = greeterconfig.Change{Staged: "login:\n  background-color: \"#ff0000\"\n"}

func serveApplyAt(t *testing.T, owner *sessionOwners, ask authorizer) (runtimeDir, runtime string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	return serveAt(t, ApplySocket, applyChange(owner.authorizeApply(ask), &sync.RWMutex{}, cfg)), filepath.Join(filepath.Dir(cfg), "runtime.yaml")
}

type polkitCalls struct {
	mu    sync.Mutex
	peers []peer
}

func (c *polkitCalls) refuse(_ context.Context, p peer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.peers = append(c.peers, p)
	return dmsocket.Refuse(dmsocket.Unauthorized, "polkit says no")
}

func (c *polkitCalls) seen() []peer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]peer(nil), c.peers...)
}

func TestApplyIsTheLoggedInUsersOrPolkits(t *testing.T) {
	owner := newSessionOwners()
	asked := &polkitCalls{}
	dir, runtime := serveApplyAt(t, owner, asked.refuse)
	me := me(t)

	_, err := ApplySocket.Send(dir, colorChange)
	if r, ok := refusal(err); !ok || r != dmsocket.Unauthorized || !strings.Contains(err.Error(), "polkit says no") {
		t.Errorf("without a session: %v, want polkit's refusal", err)
	}
	if peers := asked.seen(); len(peers) != 1 || peers[0].UID != me || peers[0].PID != int32(os.Getpid()) {
		t.Errorf("polkit was asked about %+v, want this process", peers)
	}
	owner.add(me + 1)
	if _, err := ApplySocket.Send(dir, colorChange); err == nil {
		t.Error("another user's session let this one apply")
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused change wrote runtime.yaml: %v", err)
	}

	owner.add(me)
	path, err := ApplySocket.Send(dir, colorChange)
	if err != nil {
		t.Fatalf("the session's user: %v", err)
	}
	if data, _ := os.ReadFile(path); path != runtime || !strings.Contains(string(data), "#ff0000") {
		t.Errorf("wrote %s: %q", path, data)
	}
	if n := len(asked.seen()); n != 2 {
		t.Errorf("polkit was asked %d times, want only for the users without a session", n)
	}

	owner.remove(me)
	if _, err := ApplySocket.Send(dir, colorChange); err == nil {
		t.Error("the session's end left its user able to apply")
	}
}

func TestAuthorizeApply(t *testing.T) {
	owner := newSessionOwners()
	asked := &polkitCalls{}
	authorize := owner.authorizeApply(asked.refuse)
	ctx := context.Background()
	if authorize(ctx, peer{UID: 0}) != nil || authorize(ctx, peer{UID: 1000}) == nil {
		t.Error("without a session only root may apply")
	}
	owner.add(1000)
	if authorize(ctx, peer{UID: 0}) != nil || authorize(ctx, peer{UID: 1000}) != nil || authorize(ctx, peer{UID: 1001}) == nil {
		t.Error("with 1000's session root and 1000 may apply, 1001 only through polkit")
	}
	if peers := asked.seen(); len(peers) != 2 || peers[0].UID != 1000 || peers[1].UID != 1001 {
		t.Errorf("polkit was asked about %+v, want 1000 without a session and 1001", peers)
	}
}

func TestApplyRefusesAnInvalidChangeBeforeAskingPolkit(t *testing.T) {
	asked := &polkitCalls{}
	dir, runtime := serveApplyAt(t, newSessionOwners(), asked.refuse)
	me(t)
	for name, c := range map[string]greeterconfig.Change{
		"no login keys":      {Staged: "general:\n  foo: 1\n"},
		"not YAML":           {Staged: "login: ["},
		"oversized staging":  {Staged: "show-clock: true\n" + strings.Repeat("#", greeterconfig.MaxStaged)},
		"image without data": {Staged: "login:\n  background-mode: image\n  background-image: /home/u/w.png\n"},
	} {
		_, err := ApplySocket.Send(dir, c)
		if r, ok := refusal(err); !ok || r != dmsocket.Invalid {
			t.Errorf("%s: %v, want an invalid change", name, err)
		}
	}
	if peers := asked.seen(); len(peers) != 0 {
		t.Errorf("invalid changes asked polkit about %+v", peers)
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an invalid change wrote runtime.yaml: %v", err)
	}
}

func kernelNamesPeersByPIDFD(t *testing.T) bool {
	t.Helper()
	opened := make(chan error, 1)
	s := Socket[SwitchRequest]{dmsocket.Endpoint[SwitchRequest]{Name: "pidfd.sock", MaxRequest: 64, Exchange: exchangeTimeout}}
	dir := serveAt(t, s, func(_ context.Context, p peer, _ SwitchRequest) (string, error) {
		pidfd, err := peercred.PIDFDOf(p.conn)
		if err == nil {
			err = pidfd.Close()
		}
		opened <- err
		return "", nil
	})
	if _, err := s.Send(dir, SwitchRequest{}); err != nil {
		t.Fatal(err)
	}
	err := <-opened
	if err != nil && !errors.Is(err, peercred.ErrNoPIDFD) {
		t.Fatal(err)
	}
	return err == nil
}

func openPIDFDsOfThisProcess(t *testing.T) int {
	t.Helper()
	const dir = "/proc/self/fdinfo"
	fds, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	self := "\nPid:\t" + strconv.Itoa(os.Getpid()) + "\n"
	n := 0
	for _, fd := range fds {
		info, err := os.ReadFile(filepath.Join(dir, fd.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(info), self) {
			n++
		}
	}
	return n
}

func TestApplyAsksPolkitForTheCallingProcess(t *testing.T) {
	me := me(t)
	fake := polkittest.Start(t, polkittest.Denied)
	dir, runtime := serveApplyAt(t, newSessionOwners(), askPolkit)
	refused := []struct {
		name   string
		answer polkit.AuthorizationResult
		want   dmsocket.Refusal
	}{
		{"denied", polkittest.Denied, dmsocket.Unauthorized},
		{"no agent", polkittest.Challenge, dmsocket.Unauthorized},
		{"dismissed", polkittest.Dismissed, dmsocket.Dismissed},
	}
	for _, c := range refused {
		fake.Answer(c.answer)
		_, err := ApplySocket.Send(dir, colorChange)
		if r, ok := refusal(err); !ok || r != c.want || !strings.Contains(err.Error(), string(applyAction)) {
			t.Errorf("%s: %v, want %s naming %s", c.name, err, c.want, applyAction)
		}
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unauthorized change wrote runtime.yaml: %v", err)
	}
	fake.Answer(polkittest.Authorized)
	if path, err := ApplySocket.Send(dir, colorChange); err != nil || path != runtime {
		t.Fatalf("authorized: %s, %v", path, err)
	}
	wantPIDFD := 0
	if kernelNamesPeersByPIDFD(t) {
		wantPIDFD = os.Getpid()
	}
	checks := fake.Checks.Load()
	if len(checks) != len(refused)+1 {
		t.Fatalf("polkit was asked %d times, want once per request: %+v", len(checks), checks)
	}
	for _, c := range checks {
		process, ok := c.Subject.(polkit.UnixProcess)
		if !ok || c.Action != applyAction || c.Flags != polkit.AllowUserInteraction ||
			process.PID != uint32(os.Getpid()) || process.UID != me || process.StartTime == 0 {
			t.Errorf("polkit check = %+v, want this process asked about with interaction", c)
		}
		if c.PIDFD != wantPIDFD {
			t.Errorf("the check's subject carried a pidfd of pid %d, want %d: the caller's own where the kernel gives one", c.PIDFD, wantPIDFD)
		}
	}
	if n := openPIDFDsOfThisProcess(t); n != 0 {
		t.Errorf("%d pidfds of the caller are still open after its requests were answered", n)
	}
}

func TestApplyWithoutPolkitFails(t *testing.T) {
	me(t)
	dbustest.Start(t).UseAsSystemBus(t)
	dir, _ := serveApplyAt(t, newSessionOwners(), askPolkit)
	_, err := ApplySocket.Send(dir, colorChange)
	if r, ok := refusal(err); !ok || r != dmsocket.Failed {
		t.Errorf("no polkitd: %v, want a failure", err)
	}
}

func TestThePolicyDeclaresTheAction(t *testing.T) {
	policy, err := os.ReadFile(filepath.Join("..", "..", "resources", "dev.stubbe.lys.login.policy"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), `<action id="`+string(applyAction)+`">`) {
		t.Errorf("the policy does not declare %s:\n%s", applyAction, policy)
	}
}
