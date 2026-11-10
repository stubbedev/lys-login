package dm

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/stubbedev/lys-lib/logind"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
	"github.com/stubbedev/lys-login/internal/polkitauth"
)

const applyAction polkitauth.Action = "dev.stubbe.lys.login.apply-config"

type authorizer func(ctx context.Context, p peer) error

func (o *sessionOwners) authorizeApply(askPolkit authorizer) authorizer {
	return func(ctx context.Context, p peer) error {
		if p.uid == 0 || o.runs(p.uid) {
			return nil
		}
		return askPolkit(ctx, p)
	}
}

func applyChange(authorize authorizer, gate *sync.RWMutex, configPath string) handler[greeterconfig.Change] {
	var mu sync.Mutex
	return func(ctx context.Context, p peer, c greeterconfig.Change) (string, error) {
		checked, err := c.Check()
		if err != nil {
			return "", dmsocket.Refuse(dmsocket.Invalid, err.Error())
		}
		if err := authorize(ctx, p); err != nil {
			return "", err
		}
		gate.RLock()
		defer gate.RUnlock()
		mu.Lock()
		defer mu.Unlock()
		path, err := greeterconfig.Write(checked, configPath)
		if err != nil {
			return "", err
		}
		log.Printf("dm: uid %d changed the login screen", p.uid)
		return path, nil
	}
}

func askPolkit(ctx context.Context, p peer) error {
	process, err := polkitauth.ProcessOf(p.pid, p.uid)
	if err != nil {
		return fmt.Errorf("ask polkit about uid %d: %w", p.uid, err)
	}
	dialing, cancel := context.WithTimeout(ctx, logind.CallTimeout)
	conn, err := logind.Dial(dialing)
	cancel()
	if err != nil {
		return fmt.Errorf("ask polkit about uid %d: %w", p.uid, err)
	}
	defer func() { _ = conn.Close() }()
	verdict, err := polkitauth.Check(ctx, conn, applyAction, process)
	if err != nil {
		return err
	}
	switch verdict {
	case polkitauth.Authorized:
		return nil
	case polkitauth.Dismissed:
		return dmsocket.Refuse(dmsocket.Dismissed, fmt.Sprintf("the authentication for %s was dismissed", applyAction))
	case polkitauth.NoAgent:
		return dmsocket.Refuse(dmsocket.Unauthorized, fmt.Sprintf("%s needs authentication and no polkit agent answered for uid %d", applyAction, p.uid))
	case polkitauth.Denied:
		return dmsocket.Refuse(dmsocket.Unauthorized, fmt.Sprintf("polkit does not authorize uid %d for %s", p.uid, applyAction))
	default:
		return fmt.Errorf("polkit: unknown verdict %d on %s", verdict, applyAction)
	}
}
