// Package dmsocket is the wire of lys-login's display manager sockets:
// one JSON request per connection and one Reply, the file the request
// went to or a Refusal and its reason. A client sends with an
// Endpoint; Apply is the change of the login screen any program may
// ask for.
package dmsocket

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stubbedev/lys-login/greeterconfig"
)

const DefaultRuntimeDir = "/run/lys-login"

var ErrNoDaemon = errors.New("lys-login's display manager is not running")

type Endpoint[T any] struct {
	Name       string
	MaxRequest int64
	Exchange   time.Duration
}

var Apply = Endpoint[greeterconfig.Change]{
	Name:       "apply.sock",
	MaxRequest: (greeterconfig.MaxStaged+greeterconfig.MaxImage)/3*4 + 4096,
	Exchange:   5 * time.Minute,
}

const maxReply = 64 << 10

type Refusal uint8

const (
	Failed Refusal = iota
	Invalid
	Unauthorized
	Dismissed
)

var refusalNames = [...]string{Failed: "failed", Invalid: "invalid", Unauthorized: "unauthorized", Dismissed: "dismissed"}

func (r Refusal) String() string {
	if int(r) < len(refusalNames) {
		return refusalNames[r]
	}
	return fmt.Sprintf("Refusal(%d)", uint8(r))
}

func (r Refusal) MarshalText() ([]byte, error) {
	if int(r) >= len(refusalNames) {
		return nil, fmt.Errorf("encode refusal %d: no such refusal", uint8(r))
	}
	return []byte(refusalNames[r]), nil
}

func (r *Refusal) UnmarshalText(text []byte) error {
	for i, name := range refusalNames {
		if string(text) == name {
			*r = Refusal(i)
			return nil
		}
	}
	return fmt.Errorf("decode refusal %q: no such refusal", text)
}

type RefusedError struct {
	Refusal Refusal
	Reason  string
}

func (e *RefusedError) Error() string { return e.Reason }

func Refuse(r Refusal, reason string) error { return &RefusedError{Refusal: r, Reason: reason} }

type Reply struct {
	Path    string  `json:"path,omitempty"`
	Error   string  `json:"error,omitempty"`
	Refusal Refusal `json:"refusal,omitzero"`
}

func Answer(path string, err error) Reply {
	if err == nil {
		return Reply{Path: path}
	}
	r := Reply{Error: err.Error()}
	if refused, ok := errors.AsType[*RefusedError](err); ok {
		r.Refusal = refused.Refusal
	}
	return r
}

func (e Endpoint[T]) Path(runtimeDir string) string { return filepath.Join(runtimeDir, e.Name) }

func (e Endpoint[T]) Send(runtimeDir string, req T) (string, error) {
	path := e.Path(runtimeDir)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return "", fmt.Errorf("%w (no %s)", ErrNoDaemon, path)
	}
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(e.Exchange))
	sent := json.NewEncoder(conn).Encode(req)
	if sent == nil {
		sent = conn.CloseWrite()
	}
	var r Reply
	if err := json.NewDecoder(io.LimitReader(conn, maxReply)).Decode(&r); err != nil {
		if sent != nil {
			return "", fmt.Errorf("send to %s: %w", path, sent)
		}
		return "", fmt.Errorf("read the display manager's answer on %s: %w", path, err)
	}
	if r.Error != "" {
		return "", fmt.Errorf("the display manager refused: %w", &RefusedError{Refusal: r.Refusal, Reason: r.Error})
	}
	return r.Path, nil
}
