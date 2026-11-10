package dm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	handoverVersion = 2
	workerProtocol  = 1
	maxHandover     = 8 << 20
	firstHandoverFD = 3
)

type handover struct {
	Version    int             `json:"version"`
	From       string          `json:"from"`
	RuntimeDir string          `json:"runtime_dir"`
	Client     int             `json:"client"`
	Listeners  handedListeners `json:"listeners"`
	Cards      []handedCard    `json:"cards"`
	Sessions   []handedSession `json:"sessions"`
	Rollback   rollback        `json:"rollback"`
	Failure    string          `json:"failure"`
}

type handedListeners struct {
	Apply   int `json:"apply"`
	Cursor  int `json:"cursor"`
	Splash  int `json:"splash"`
	Upgrade int `json:"upgrade"`
	Switch  int `json:"switch"`
}

type handedCard struct {
	Path string `json:"path"`
	FD   int    `json:"fd"`
}

type handedSession struct {
	WorkerPID int      `json:"worker_pid"`
	VT        int      `json:"vt"`
	User      string   `json:"user"`
	ID        string   `json:"id"`
	Channel   int      `json:"channel"`
	Protocol  int      `json:"protocol"`
	Unread    []byte   `json:"unread"`
	UID       *uint32  `json:"uid"`
	Cmd       []string `json:"cmd"`
}

type rollback struct {
	Exe  int      `json:"exe"`
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func (h handover) encode() ([]byte, error) {
	if err := h.validate(); err != nil {
		return nil, fmt.Errorf("the state to hand over: %w", err)
	}
	data, err := json.Marshal(h)
	if err != nil {
		return nil, fmt.Errorf("encode the state to hand over: %w", err)
	}
	if len(data) > maxHandover {
		return nil, fmt.Errorf("the state to hand over is %d bytes, over the %d-byte limit", len(data), maxHandover)
	}
	return data, nil
}

func parseHandover(data []byte) (handover, error) {
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return handover{}, fmt.Errorf("the handed-over state: %w", err)
	}
	if head.Version != handoverVersion {
		return handover{}, fmt.Errorf("the handed-over state has version %d, and this build reads version %d", head.Version, handoverVersion)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var h handover
	if err := dec.Decode(&h); err != nil {
		return handover{}, fmt.Errorf("the handed-over state: %w", err)
	}
	if err := h.validate(); err != nil {
		return handover{}, fmt.Errorf("the handed-over state: %w", err)
	}
	return h, nil
}

func (h handover) sameRuntimeDir(o Options) error {
	if filepath.Clean(o.RuntimeDir) != filepath.Clean(h.RuntimeDir) {
		return fmt.Errorf("the sockets handed over are in %s, and this daemon is to run in %s", h.RuntimeDir, o.RuntimeDir)
	}
	return nil
}

func (h handover) validate() error {
	if h.Version != handoverVersion {
		return fmt.Errorf("version %d, not %d", h.Version, handoverVersion)
	}
	if !filepath.IsAbs(h.From) {
		return fmt.Errorf("the old program %q is not an absolute path", h.From)
	}
	if !filepath.IsAbs(h.RuntimeDir) {
		return fmt.Errorf("the runtime directory %q is not an absolute path", h.RuntimeDir)
	}
	if len(h.Rollback.Args) == 0 {
		return errors.New("no arguments to roll back with")
	}
	for _, kv := range h.Rollback.Env {
		if !strings.Contains(kv, "=") {
			return fmt.Errorf("the environment entry %q to roll back with is not KEY=value", kv)
		}
	}
	for _, card := range h.Cards {
		if !filepath.IsAbs(card.Path) {
			return fmt.Errorf("the DRM card %q is not an absolute path", card.Path)
		}
	}
	vts := map[int]bool{}
	for _, s := range h.Sessions {
		if s.VT <= 0 || vts[s.VT] {
			return fmt.Errorf("the session of worker %d is on VT %d, which is none or another session's", s.WorkerPID, s.VT)
		}
		vts[s.VT] = true
		if s.User == "" {
			return fmt.Errorf("the session of worker %d names no user", s.WorkerPID)
		}
		if s.WorkerPID <= 1 {
			return fmt.Errorf("the session's worker has pid %d", s.WorkerPID)
		}
		if s.Protocol != workerProtocol {
			return fmt.Errorf("the session's worker speaks protocol %d, and this build speaks %d", s.Protocol, workerProtocol)
		}
		if len(s.Cmd) == 0 {
			return errors.New("the session has no command")
		}
	}
	seen := map[int]string{}
	for _, f := range h.descriptors() {
		if f.fd < firstHandoverFD {
			return fmt.Errorf("%s is on descriptor %d, below %d", f.what, f.fd, firstHandoverFD)
		}
		if other, dup := seen[f.fd]; dup {
			return fmt.Errorf("%s and %s are both on descriptor %d", other, f.what, f.fd)
		}
		seen[f.fd] = f.what
	}
	return nil
}

type namedFD struct {
	what string
	fd   int
}

func (h handover) descriptors() []namedFD {
	fds := []namedFD{
		{"the upgrade's client", h.Client},
		{ApplySocket.Name, h.Listeners.Apply},
		{CursorSocket.Name, h.Listeners.Cursor},
		{SplashSocket.Name, h.Listeners.Splash},
		{UpgradeSocket.Name, h.Listeners.Upgrade},
		{SwitchSocket.Name, h.Listeners.Switch},
		{"the old program", h.Rollback.Exe},
	}
	for _, card := range h.Cards {
		fds = append(fds, namedFD{card.Path, card.FD})
	}
	for _, s := range h.Sessions {
		fds = append(fds, namedFD{fmt.Sprintf("the channel of the session on VT %d", s.VT), s.Channel})
	}
	return fds
}
