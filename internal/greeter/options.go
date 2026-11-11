// Package greeter is `lys-login greet`: the login screen in its greeter
// mode, as a greetd greeter. It runs as the single client of a kiosk
// compositor (cage) that greetd spawns, drives the login over the
// greetd IPC socket, and exits when greetd starts the chosen session.
package greeter

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-lib/atomicfile"
	"github.com/stubbedev/lys-lib/xdg"
)

// The directories of session files under a data directory.
const (
	waylandSessionsDir = "wayland-sessions"
	xSessionsDir       = "xsessions"
)

const (
	stateApp        = "lys-greeter"
	lastSessionFile = "last-session"
)

// defaultStatePath is the last resort for the remembered session when
// neither --state, XDG_STATE_HOME, nor HOME yields a location.
const defaultStatePath = "/var/lib/" + stateApp + "/" + lastSessionFile

// Options are the parsed command line (config.rs Options).
type Options struct {
	// ConfigPath is the lys-shell config the greeter themes from.
	ConfigPath string
	// SessionDirs and XSessionDirs are scanned for session files.
	SessionDirs, XSessionDirs []string
	// StatePath remembers the last session id; the last username sits
	// in a last-user file beside it.
	StatePath string
	// Command is the explicit fallback session from `-- <argv>`.
	Command []string
	// Env holds extra KEY=value entries for the session.
	Env      []string
	HandOver bool
}

// ApplyCacheDir keeps the greeter's caches beside its state, the one
// place its user can write on every setup: greetd's greeter account often
// has an unwritable home such as /var/empty, and the system font scan
// fails without a cache directory.
func ApplyCacheDir(statePath string) error {
	dir := filepath.Join(filepath.Dir(statePath), "cache")
	if err := os.Setenv("XDG_CACHE_HOME", dir); err != nil {
		return fmt.Errorf("greeter: cache dir %q: %w", dir, err)
	}
	return nil
}

// UsageError is a malformed command line; the greeter exits 2 with it.
type UsageError struct{ detail string }

func (e UsageError) Error() string {
	return e.detail + "\nusage: lys-login greet [--config PATH] [--sessions DIR]... [--xsessions DIR]... " +
		"[--state PATH] [--env KEY=VAL]... [--hand-over] [-- <session argv...>]"
}

// ParseOptions parses the arguments after the program name, taking the
// defaults of --sessions, --xsessions and --state from env. Everything
// after "--" is the fallback session argv; the repeatable --sessions
// and --xsessions replace their defaults once given.
func ParseOptions(env xdg.Env, args []string) (Options, error) {
	o := Options{ConfigPath: config.GreeterConfigPath}
	value := func(i *int, flag, need string) (string, error) {
		if *i+1 >= len(args) {
			return "", UsageError{flag + " requires " + need}
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		var err error
		var v string
		switch args[i] {
		case "--":
			o.Command = append(o.Command, args[i+1:]...)
			i = len(args)
		case "--config":
			v, err = value(&i, "--config", "a path")
			o.ConfigPath = v
		case "--sessions":
			v, err = value(&i, "--sessions", "a DIR")
			o.SessionDirs = append(o.SessionDirs, v)
		case "--xsessions":
			v, err = value(&i, "--xsessions", "a DIR")
			o.XSessionDirs = append(o.XSessionDirs, v)
		case "--state":
			v, err = value(&i, "--state", "a path")
			o.StatePath = v
		case "--hand-over":
			o.HandOver = true
		case "--env":
			v, err = value(&i, "--env", "KEY=VAL")
			if err == nil && !strings.Contains(v, "=") {
				err = UsageError{"--env value must be KEY=VAL"}
			}
			o.Env = append(o.Env, v)
		default:
			err = UsageError{"unexpected argument: " + args[i]}
		}
		if err != nil {
			return Options{}, err
		}
	}
	if len(o.SessionDirs) == 0 {
		o.SessionDirs = defaultSessionDirs(env, waylandSessionsDir)
	}
	if len(o.XSessionDirs) == 0 {
		o.XSessionDirs = defaultSessionDirs(env, xSessionsDir)
	}
	if o.StatePath == "" {
		o.StatePath = defaultStateFile(env)
	}
	return o, nil
}

// defaultSessionDirs are the name directories of env's system data
// directories, highest precedence first: the discovery defaults gdm
// reads, and sddm's while XDG_DATA_DIRS names none. The data home is
// left out: the greeter runs as its own user, whose home installs no
// sessions.
func defaultSessionDirs(env xdg.Env, name string) []string {
	data := env.DataDirs()
	dirs := make([]string, len(data))
	for i, dir := range data {
		dirs[i] = filepath.Join(dir, name)
	}
	return dirs
}

// defaultStateFile: $XDG_STATE_HOME/lys-greeter/last-session, else
// $HOME/.local/state/..., else /var/lib/lys-greeter/last-session; a
// variable that holds no absolute path counts as unset. greetd's
// greeter user has an unusual HOME, so an explicit --state is the
// robust choice.
func defaultStateFile(env xdg.Env) string {
	dir, ok := env.StateDir(stateApp)
	if !ok {
		return defaultStatePath
	}
	return filepath.Join(dir, lastSessionFile)
}

// LastUserPath is the last-user file beside the session state file.
func LastUserPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "last-user")
}

// LoadLast reads a remembered value; "" when unset or unreadable.
func LoadLast(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // the greeter's own state file
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveLast persists a remembered value, creating its directory and replacing the
// file whole, so a crash leaves the old value or the new one.
func SaveLast(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the greeter's state dir is not secret
		return fmt.Errorf("greeter: create the directory of %s: %w", path, err)
	}
	if err := atomicfile.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("greeter: write %s: %w", path, err)
	}
	return nil
}
