package dm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
	"github.com/stubbedev/lys-login/internal/polkitauth/polkitauthtest"
)

var colorChange = greeterconfig.Change{Staged: "login:\n  background-color: \"#ff0000\"\n"}

func serveApplyAt(t *testing.T, owner *sessionOwners, polkit authorizer) (runtimeDir, runtime string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	return serveAt(t, ApplySocket, applyChange(owner.authorizeApply(polkit), &sync.RWMutex{}, cfg)), filepath.Join(filepath.Dir(cfg), "runtime.yaml")
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
	polkit := &polkitCalls{}
	dir, runtime := serveApplyAt(t, owner, polkit.refuse)
	me := me(t)

	_, err := ApplySocket.Send(dir, colorChange)
	if r, ok := refusal(err); !ok || r != dmsocket.Unauthorized || !strings.Contains(err.Error(), "polkit says no") {
		t.Errorf("without a session: %v, want polkit's refusal", err)
	}
	if peers := polkit.seen(); len(peers) != 1 || peers[0].uid != me || peers[0].pid != int32(os.Getpid()) {
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
	if n := len(polkit.seen()); n != 2 {
		t.Errorf("polkit was asked %d times, want only for the users without a session", n)
	}

	owner.remove(me)
	if _, err := ApplySocket.Send(dir, colorChange); err == nil {
		t.Error("the session's end left its user able to apply")
	}
}

func TestAuthorizeApply(t *testing.T) {
	owner := newSessionOwners()
	polkit := &polkitCalls{}
	authorize := owner.authorizeApply(polkit.refuse)
	ctx := context.Background()
	if authorize(ctx, peer{uid: 0}) != nil || authorize(ctx, peer{uid: 1000}) == nil {
		t.Error("without a session only root may apply")
	}
	owner.add(1000)
	if authorize(ctx, peer{uid: 0}) != nil || authorize(ctx, peer{uid: 1000}) != nil || authorize(ctx, peer{uid: 1001}) == nil {
		t.Error("with 1000's session root and 1000 may apply, 1001 only through polkit")
	}
	if peers := polkit.seen(); len(peers) != 2 || peers[0].uid != 1000 || peers[1].uid != 1001 {
		t.Errorf("polkit was asked about %+v, want 1000 without a session and 1001", peers)
	}
}

func TestApplyRefusesAnInvalidChangeBeforeAskingPolkit(t *testing.T) {
	polkit := &polkitCalls{}
	dir, runtime := serveApplyAt(t, newSessionOwners(), polkit.refuse)
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
	if peers := polkit.seen(); len(peers) != 0 {
		t.Errorf("invalid changes asked polkit about %+v", peers)
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an invalid change wrote runtime.yaml: %v", err)
	}
}

func TestApplyAsksPolkitForTheCallingProcess(t *testing.T) {
	me := me(t)
	fake := polkitauthtest.Start(t, polkitauthtest.Denied)
	dir, runtime := serveApplyAt(t, newSessionOwners(), askPolkit)
	for _, c := range []struct {
		name   string
		answer polkitauthtest.Result
		want   dmsocket.Refusal
	}{
		{"denied", polkitauthtest.Denied, dmsocket.Unauthorized},
		{"no agent", polkitauthtest.Challenge, dmsocket.Unauthorized},
		{"dismissed", polkitauthtest.Dismissed, dmsocket.Dismissed},
	} {
		fake.Answer(c.answer)
		_, err := ApplySocket.Send(dir, colorChange)
		if r, ok := refusal(err); !ok || r != c.want || !strings.Contains(err.Error(), string(applyAction)) {
			t.Errorf("%s: %v, want %s naming %s", c.name, err, c.want, applyAction)
		}
	}
	if _, err := os.Stat(runtime); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unauthorized change wrote runtime.yaml: %v", err)
	}
	fake.Answer(polkitauthtest.Authorized)
	if path, err := ApplySocket.Send(dir, colorChange); err != nil || path != runtime {
		t.Fatalf("authorized: %s, %v", path, err)
	}
	for _, c := range fake.Checks.Load() {
		d := c.Subject.Details
		if c.Action != string(applyAction) || c.Flags != 1 || c.Subject.Kind != "unix-process" ||
			d["pid"].Value() != uint32(os.Getpid()) || d["uid"].Value() != int32(me) || d["start-time"].Value() == uint64(0) {
			t.Errorf("polkit check = %+v", c)
		}
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
