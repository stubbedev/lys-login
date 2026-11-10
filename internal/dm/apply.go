package dm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/stubbedev/lys-lib/peercred"
	"github.com/stubbedev/lys-lib/polkit"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
)

const applyAction polkit.Action = "dev.stubbe.lys.login.apply-config"

type authorizer func(ctx context.Context, p peer) error

func (o *sessionOwners) authorizeApply(askPolkit authorizer) authorizer {
	return func(ctx context.Context, p peer) error {
		if p.UID == 0 || o.runs(p.UID) {
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
		log.Printf("dm: uid %d changed the login screen", p.UID)
		return path, nil
	}
}

func (p peer) process() (polkit.UnixProcess, error) {
	pidfd, err := peercred.PIDFDOf(p.conn)
	if errors.Is(err, peercred.ErrNoPIDFD) {
		return polkit.ProcessOf(p.Credentials)
	}
	if err != nil {
		return polkit.UnixProcess{}, err
	}
	process, err := polkit.ProcessOfPIDFD(pidfd)
	if err != nil {
		return polkit.UnixProcess{}, errors.Join(err, pidfd.Close())
	}
	return process, nil
}

func askPolkit(ctx context.Context, p peer) error {
	process, err := p.process()
	if err != nil {
		return fmt.Errorf("ask polkit about uid %d: %w", p.UID, err)
	}
	if process.PIDFD != nil {
		defer func() {
			if err := process.PIDFD.Close(); err != nil {
				log.Printf("dm: ask polkit about uid %d: %v", p.UID, err)
			}
		}()
	}
	conn, err := polkit.Dial(ctx)
	if err != nil {
		return fmt.Errorf("ask polkit about uid %d: %w", p.UID, err)
	}
	defer func() { _ = conn.Close() }()
	result, err := polkit.NewAuthority(conn).CheckAuthorization(ctx, process, applyAction, polkit.AllowUserInteraction)
	if err != nil {
		return err
	}
	switch verdict := result.Verdict(); verdict {
	case polkit.Authorized:
		return nil
	case polkit.Dismissed:
		return dmsocket.Refuse(dmsocket.Dismissed, fmt.Sprintf("the authentication for %s was dismissed", applyAction))
	case polkit.Challenge:
		return dmsocket.Refuse(dmsocket.Unauthorized, fmt.Sprintf("%s needs authentication and no polkit agent answered for uid %d", applyAction, p.UID))
	case polkit.Denied:
		return dmsocket.Refuse(dmsocket.Unauthorized, fmt.Sprintf("polkit does not authorize uid %d for %s", p.UID, applyAction))
	default:
		return fmt.Errorf("polkit: unknown verdict %s on %s", verdict, applyAction)
	}
}
