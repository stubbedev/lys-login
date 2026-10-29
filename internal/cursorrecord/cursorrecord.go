// Package cursorrecord is the record of the cursor each user's last
// session showed, which the display manager keeps for the greeter. The
// greeter runs before anyone logs in, as an account that cannot read
// into a 0700 home, and a session's XDG_STATE_HOME is unknown before
// it starts; so the session hands its cursor to the display manager
// (`lys-login record-cursor`), which writes it, world-readable, under
// Dir by the session's uid.
package cursorrecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/stubbedev/lys-lib/atomicfile"
	"github.com/stubbedev/lys-lib/xdg"
)

// Dir is where the display manager keeps the records.
const Dir = "/var/lib/lys-login/cursor"

// maxTheme bounds a theme's name: one directory name.
const maxTheme = 255

// Cursor is a cursor theme and size; either may be unknown, not both.
// Decoding one checks it, so a decoded Cursor is always one to record.
type Cursor struct {
	// Theme is the xcursor theme, a directory name; "" when unknown.
	Theme string `json:"theme,omitempty"`
	// Size is the nominal size in pixels, 0 when unknown.
	Size uint32 `json:"size,omitempty"`
}

// check reports why c cannot be recorded: it names nothing, or a theme
// that is not a single directory name.
func (c Cursor) check() error {
	if c == (Cursor{}) {
		return errors.New("cursor record: names neither a theme nor a size")
	}
	if c.Theme == "" {
		return nil
	}
	if len(c.Theme) > maxTheme || c.Theme == "." || c.Theme == ".." ||
		strings.ContainsFunc(c.Theme, func(r rune) bool { return r == '/' || unicode.IsControl(r) }) {
		return fmt.Errorf("cursor record: theme %q is not a directory name", c.Theme)
	}
	return nil
}

// UnmarshalJSON decodes a cursor and refuses one that cannot be
// recorded.
func (c *Cursor) UnmarshalJSON(data []byte) error {
	type plain Cursor
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if err := Cursor(p).check(); err != nil {
		return err
	}
	*c = Cursor(p)
	return nil
}

// FromEnv is the cursor a session with environment env shows:
// XCURSOR_THEME, and XCURSOR_SIZE else HYPRCURSOR_SIZE, which the
// compositor exported. False when env names neither; an error when it
// names a theme that cannot be recorded.
func FromEnv(env xdg.Env) (Cursor, bool, error) {
	size := env("XCURSOR_SIZE")
	if size == "" {
		size = env("HYPRCURSOR_SIZE")
	}
	c := Cursor{Theme: env("XCURSOR_THEME"), Size: parseSize(size)}
	if c == (Cursor{}) {
		return Cursor{}, false, nil
	}
	if err := c.check(); err != nil {
		return Cursor{}, false, err
	}
	return c, true, nil
}

// parseSize is a positive size in pixels, 0 for anything else.
func parseSize(s string) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// path is uid's record under dir.
func path(dir string, uid uint32) string {
	return filepath.Join(dir, strconv.FormatUint(uint64(uid), 10))
}

// Write replaces uid's record under dir with c, readable by every
// account, and returns its path.
func Write(dir string, uid uint32, c Cursor) (string, error) {
	if err := c.check(); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("cursor record: encode %+v: %w", c, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the greeter account reads the records
		return "", fmt.Errorf("cursor record: create %s: %w", dir, err)
	}
	p := path(dir, uid)
	if err := atomicfile.WriteFile(p, append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("cursor record: write %s: %w", p, err)
	}
	return p, nil
}

// Read is uid's record under dir; an error matching fs.ErrNotExist
// when uid has none.
func Read(dir string, uid uint32) (Cursor, error) {
	p := path(dir, uid)
	data, err := os.ReadFile(p) //nolint:gosec // a record the display manager wrote
	if err != nil {
		return Cursor{}, fmt.Errorf("cursor record: %w", err)
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return Cursor{}, fmt.Errorf("cursor record: decode %s: %w", p, err)
	}
	return c, nil
}
