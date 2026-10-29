package greeter

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stubbedev/lys-shell/config"
	"github.com/stubbedev/lys-ui/gtksettings"

	"github.com/stubbedev/lys-login/internal/cursorrecord"
)

// The greeter runs pre-login, so there is no session to ask what
// cursor the user sees (cursor.rs). The record the user's last session
// handed the display manager comes first (cursorrecord); then the
// user's own dotfiles, best effort, as only a home the greeter account
// may enter yields them: the compositor matching the remembered session
// first (Hyprland env and hyprctl setcursor, niri's cursor block,
// sway's seat xcursor_theme), then GTK settings.ini and
// ~/.icons/default/index.theme.
//
// Like GTK 4.22, lys-ui asks the compositor to draw the cursor
// (cursor-shape-v1) when it can; on a host without the protocol (cage)
// it draws xcursor themes client-side from XCURSOR_THEME and
// XCURSOR_SIZE, so the resolved cursor is applied by exporting those
// before connecting. The bundled-PNG fallback the Rust greeter works
// around does not exist here.

// Cursor is what one source found; either side may be missing.
type Cursor struct {
	Theme string // "" when unknown
	Size  uint32 // 0 when unknown
}

// or fills the missing sides from other.
func (c Cursor) or(other Cursor) Cursor {
	if c.Theme == "" {
		c.Theme = other.Theme
	}
	if c.Size == 0 {
		c.Size = other.Size
	}
	return c
}

func (c Cursor) complete() bool { return c.Theme != "" && c.Size != 0 }

// DetectCursor is the cursor u's session shows: the record under
// records, then the dotfiles in u's home; session is the remembered
// session id, whose compositor is consulted first.
func DetectCursor(records string, u User, session string) Cursor {
	home := u.Home
	read := func(rel string) string {
		data, _ := os.ReadFile(filepath.Join(home, rel)) //nolint:gosec // best-effort dotfile read
		return string(data)
	}
	found := recordedCursor(records, u.UID)
	if found.complete() {
		return found
	}
	session = strings.ToLower(session)
	order := []string{"hypr", "niri", "sway"}
	for i, c := range order {
		if strings.Contains(session, c) {
			order = append([]string{c}, append(order[:i:i], order[i+1:]...)...)
			break
		}
	}
	for _, c := range order {
		if found.complete() {
			break
		}
		switch c {
		case "hypr":
			found = found.or(hyprlandFile(filepath.Join(home, ".config/hypr/hyprland.conf"), home, 0))
		case "niri":
			found = found.or(parseNiri(read(".config/niri/config.kdl")))
		default:
			found = found.or(parseSway(read(".config/sway/config")))
		}
	}
	for _, text := range []func() Cursor{
		func() Cursor { return parseGTKSettings(read(".config/gtk-4.0/settings.ini")) },
		func() Cursor { return parseGTKSettings(read(".config/gtk-3.0/settings.ini")) },
		func() Cursor { return parseIndexTheme(read(".icons/default/index.theme")) },
	} {
		if found.complete() {
			break
		}
		found = found.or(text())
	}
	return found
}

// cursorUser is whose cursor the greeter shows: the remembered user's,
// else the only user's; false for neither.
func cursorUser(users []User, lastUser string) (User, bool) {
	for _, u := range users {
		if u.Name == lastUser && lastUser != "" {
			return u, true
		}
	}
	if len(users) == 1 {
		return users[0], true
	}
	return User{}, false
}

// detectLastCursor is the cursor cursorUser's last session showed,
// none without such a user.
func detectLastCursor(init Init) Cursor {
	u, ok := cursorUser(init.Users, init.LastUser)
	if !ok {
		return Cursor{}
	}
	return DetectCursor(cursorrecord.Dir, u, init.LastSession)
}

// KioskCursor is the cursor the display manager has the greeter's
// kiosk compositor draw: ResolveCursor over the record the last
// session of lastUser, else of the only user, left under records. It
// reads no dotfiles: the display manager runs as root, and opens
// nothing a user controls.
func KioskCursor(g config.LoginGreeterConfig, records string, users []User, lastUser string) Cursor {
	var recorded Cursor
	if u, ok := cursorUser(users, lastUser); ok {
		recorded = recordedCursor(records, u.UID)
	}
	return ResolveCursor(g, recorded)
}

// ResolveCursor applies install_cursor's order: an explicit [greeter]
// key (config.yaml or runtime.yaml), the detected dotfiles, the
// XCURSOR_* environment, then the size default. The theme may stay "".
func ResolveCursor(g config.LoginGreeterConfig, detected Cursor) Cursor {
	var configured Cursor
	if g.CursorThemeExplicit {
		configured.Theme = g.CursorTheme
	}
	if g.CursorSizeExplicit {
		configured.Size = g.CursorSize
	}
	env := Cursor{Theme: os.Getenv("XCURSOR_THEME")}
	if n, err := strconv.ParseUint(strings.TrimSpace(os.Getenv("XCURSOR_SIZE")), 10, 32); err == nil {
		env.Size = uint32(n)
	}
	resolved := configured.or(detected).or(env)
	if resolved.Size == 0 {
		resolved.Size = max(g.CursorSize, 1)
	}
	return resolved
}

// ApplyCursor exports the resolved cursor for lys-ui's theme loader.
func ApplyCursor(c Cursor) {
	if c.Theme != "" {
		_ = os.Setenv("XCURSOR_THEME", c.Theme)
	}
	_ = os.Setenv("XCURSOR_SIZE", strconv.FormatUint(uint64(c.Size), 10))
}

// recordedCursor is the cursor uid's last session recorded under
// records; none when it left no record or the record cannot be read,
// which is logged.
func recordedCursor(records string, uid uint32) Cursor {
	c, err := cursorrecord.Read(records, uid)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("greeter: %v", err)
		}
		return Cursor{}
	}
	return Cursor(c)
}

func parseSize(s string) uint32 {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

// hyprlandFile reads a Hyprland config, following `source =` includes
// up to depth 3 (globs are not expanded).
func hyprlandFile(path, home string, depth int) Cursor {
	if depth > 3 {
		return Cursor{}
	}
	data, err := os.ReadFile(path) //nolint:gosec // best-effort dotfile read
	if err != nil {
		return Cursor{}
	}
	found, sources := parseHyprland(string(data))
	for _, src := range sources {
		if found.complete() {
			break
		}
		var resolved string
		switch {
		case strings.HasPrefix(src, "~/"):
			resolved = filepath.Join(home, src[2:])
		case strings.HasPrefix(src, "$HOME/"):
			resolved = filepath.Join(home, src[6:])
		case strings.HasPrefix(src, "/"):
			resolved = src
		default:
			resolved = filepath.Join(filepath.Dir(path), src)
		}
		found = found.or(hyprlandFile(resolved, home, depth+1))
	}
	return found
}

// parseHyprland extracts env = XCURSOR_* lines, exec[-once] = hyprctl
// setcursor <theme> <size>, and the source includes.
func parseHyprland(text string) (Cursor, []string) {
	var c Cursor
	var sources []string
	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "env", "envd":
			name, val, ok := strings.Cut(value, ",")
			if !ok {
				continue
			}
			switch strings.TrimSpace(name) {
			case "XCURSOR_THEME":
				if c.Theme == "" {
					c.Theme = strings.TrimSpace(val)
				}
			case "XCURSOR_SIZE":
				if c.Size == 0 {
					c.Size = parseSize(val)
				}
			}
		case "exec", "exec-once":
			tok := strings.Fields(value)
			if len(tok) >= 2 && strings.HasSuffix(tok[0], "hyprctl") && tok[1] == "setcursor" {
				if len(tok) > 2 && c.Theme == "" {
					c.Theme = strings.Trim(tok[2], `"`)
				}
				if len(tok) > 3 && c.Size == 0 {
					c.Size = parseSize(tok[3])
				}
			}
		case "source":
			sources = append(sources, value)
		}
	}
	return c, sources
}

// parseNiri scans niri's KDL for xcursor-theme and xcursor-size.
func parseNiri(text string) Cursor {
	var c Cursor
	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "//")
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "xcursor-theme"); ok {
			if v := strings.Trim(strings.TrimSpace(rest), `"`); c.Theme == "" && v != "" {
				c.Theme = v
			}
		} else if rest, ok := strings.CutPrefix(line, "xcursor-size"); ok && c.Size == 0 {
			c.Size = parseSize(rest)
		}
	}
	return c
}

// parseSway reads `seat <name> xcursor_theme <theme> [size]`.
func parseSway(text string) Cursor {
	var c Cursor
	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		tok := strings.Fields(line)
		if len(tok) < 4 || tok[0] != "seat" || tok[2] != "xcursor_theme" {
			continue
		}
		if c.Theme == "" {
			c.Theme = strings.Trim(tok[3], `"`)
		}
		if c.Size == 0 && len(tok) > 4 {
			c.Size = parseSize(tok[4])
		}
	}
	return c
}

// parseGTKSettings reads gtk-cursor-theme-name/-size.
func parseGTKSettings(text string) Cursor {
	s := gtksettings.Parse(text)
	return Cursor{Theme: s.CursorThemeName, Size: s.CursorThemeSize}
}

// parseIndexTheme reads the Inherits= of ~/.icons/default/index.theme
// (theme only).
func parseIndexTheme(text string) Cursor {
	for line := range strings.SplitSeq(text, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "Inherits" {
			continue
		}
		first, _, _ := strings.Cut(value, ",")
		return Cursor{Theme: strings.TrimSpace(first)}
	}
	return Cursor{}
}
