// Package dm is the login screen's display manager: `lys-login daemon`,
// run as root by its system service on a VT, shows the greeter there and
// starts the session a user logs in to. It needs PAM with pam_systemd
// and a running logind, which register each session, and lys-wm, which
// hosts the greeter. It replaces greetd and cage, and takes the logged-in
// user's changes of the login screen without polkit.
//
// The daemon itself never authenticates or opens a session: each greeter
// and each login runs in a worker, `lys-login worker`, a root process of
// its own that holds the PAM transaction from start to end and leads the
// logind session pam_systemd registers. The greeter runs as the
// unprivileged greeter user inside lys-wm, configured as a kiosk, and
// speaks the greetd protocol to the daemon over a socket only it can
// reach, so the greeter UI is the same one that runs under greetd.
package dm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/stubbedev/lys-login/internal/auth"
)

// A worker and the daemon exchange JSON lines over a socket pair: the
// daemon sends requests, the worker answers with events.

// request is the daemon's side.
type request struct {
	Op string `json:"op"`
	// User is the account to authenticate (auth).
	User string `json:"user,omitempty"`
	// Answer is the reply to the worker's last prompt (answer); nil
	// answers an info or error message, or cancels an input prompt.
	Answer *string `json:"answer,omitempty"`
	// Cmd and Env are the session to start (start).
	Cmd []string `json:"cmd,omitempty"`
	Env []string `json:"env,omitempty"`
}

// The request ops.
const (
	opAuth   = "auth"
	opAnswer = "answer"
	opStart  = "start"
	opCancel = "cancel"
)

// event is the worker's side.
type event struct {
	Kind string `json:"kind"`
	// Prompt is a PAM message to show (prompt).
	Prompt *auth.Prompt `json:"prompt,omitempty"`
	// Text is why the login failed (failed), how the session ended
	// (ended), or why its keyring stays locked (keyring-locked).
	Text string `json:"text,omitempty"`
	// UID is the account the session runs as (started).
	UID       uint32 `json:"uid"`
	SessionID string `json:"session_id,omitempty"`
}

// The event kinds.
const (
	evPrompt        = "prompt"
	evAuthenticated = "authenticated"
	evFailed        = "failed"
	evStarted       = "started"
	evEnded         = "ended"
	evKeyringLocked = "keyring-locked"
)

// channel is one side of a worker's socket pair.
type channel struct {
	r       *bufio.Reader
	backlog []byte
	partial []byte
	mu      sync.Mutex
	w       io.Writer
}

// maxLine bounds one message: the largest is a session's command and
// environment.
const maxLine = 1 << 20

func newChannel(rw io.ReadWriter) *channel {
	return resumeChannel(rw, nil)
}

func resumeChannel(rw io.ReadWriter, unread []byte) *channel {
	return &channel{r: bufio.NewReaderSize(rw, 4096), backlog: unread, w: rw}
}

func (c *channel) send(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(line, '\n'))
	return err
}

// errClosed is the other side going away.
var errClosed = errors.New("dm: the other side closed the channel")

func (c *channel) recv(v any) error {
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("dm: decode %q: %w", line, err)
	}
	return nil
}

func (c *channel) readLine() ([]byte, error) {
	if i := bytes.IndexByte(c.backlog, '\n'); i >= 0 {
		line := c.backlog[:i]
		c.backlog = c.backlog[i+1:]
		return line, nil
	}
	c.partial, c.backlog = append(c.partial, c.backlog...), nil
	for {
		fragment, err := c.r.ReadSlice('\n')
		c.partial = append(c.partial, fragment...)
		if len(c.partial) > maxLine {
			return nil, fmt.Errorf("dm: read: a message over the %d-byte limit", maxLine)
		}
		switch {
		case err == nil:
			line := bytes.TrimSuffix(c.partial, []byte{'\n'})
			c.partial = nil
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return nil, errClosed
		default:
			return nil, fmt.Errorf("dm: read: %w", err)
		}
	}
}

func (c *channel) unread() []byte {
	return slices.Concat(c.backlog, c.partial)
}
