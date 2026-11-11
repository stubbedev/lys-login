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

// SystemStatePath is the state file of the greeter the display manager
// runs: under the directory every package gives the greeter's account.
const SystemStatePath = "/var/lib/" + stateApp + "/" + lastSessionFile

// Defaults are what a command line leaves unsaid: the session
// directories and the state file.
type Defaults struct {
	dataDirs  []string
	statePath string
	stateErr  error
}

// UserDefaults are the defaults of a greeter its own account runs, as
// under greetd: the session directories of env's system data
// directories, and the state file under env's state directory. The data
// home is left out: the greeter's account installs no sessions. Where
// env names no absolute state directory, a command line without --state
// is refused.
func UserDefaults(env xdg.Env) Defaults {
	d := Defaults{dataDirs: env.DataDirs()}
	dir, ok := env.StateDir(stateApp)
	if !ok {
		d.stateErr = UsageError{"--state is not given, and neither XDG_STATE_HOME nor HOME holds an absolute path to keep the last session under"}
		return d
	}
	d.statePath = filepath.Join(dir, lastSessionFile)
	return d
}

// SystemDefaults are the defaults the display manager resolves for the
// greeter it starts: the session directories of env's system data
// directories and SystemStatePath. The display manager's own home and
// state directory are root's, where the greeter's account cannot write,
// so they take no part.
func SystemDefaults(env xdg.Env) Defaults {
	return Defaults{dataDirs: env.DataDirs(), statePath: SystemStatePath}
}

func (d Defaults) sessionDirs(name string) []string {
	dirs := make([]string, len(d.dataDirs))
	for i, dir := range d.dataDirs {
		dirs[i] = filepath.Join(dir, name)
	}
	return dirs
}

// Options are the parsed command line (config.rs Options).
type Options struct {
	// ConfigPath is the lys-shell config the greeter themes from.
	ConfigPath string
	// SessionDirs and XSessionDirs are scanned for session files; none
	// offers no session of that kind.
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
	return e.detail + "\nusage: lys-login greet [--config PATH] [--sessions DIR]... [--no-sessions] [--xsessions DIR]... [--no-xsessions] " +
		"[--state PATH] [--env KEY=VAL]... [--hand-over] [-- <session argv...>]"
}

// The command line's flags.
const (
	configFlag      = "--config"
	sessionsFlag    = "--sessions"
	noSessionsFlag  = "--no-sessions"
	xSessionsFlag   = "--xsessions"
	noXSessionsFlag = "--no-xsessions"
	stateFlag       = "--state"
	envFlag         = "--env"
	handOverFlag    = "--hand-over"
	commandFlag     = "--"
)

type sessionDirFlags struct {
	dirs []string
	none bool
}

func (f sessionDirFlags) resolved(dirFlag, noneFlag string, defaults []string) ([]string, error) {
	switch {
	case f.none && len(f.dirs) > 0:
		return nil, UsageError{noneFlag + " contradicts " + dirFlag + " " + f.dirs[0]}
	case f.none:
		return nil, nil
	case len(f.dirs) > 0:
		return f.dirs, nil
	}
	return defaults, nil
}

// ParseOptions parses the arguments after the program name, taking
// what they leave unsaid from d. Everything after "--" is the fallback
// session argv; the repeatable --sessions and --xsessions replace their
// defaults once given, and --no-sessions and --no-xsessions replace
// them with nothing. Every path is absolute: the greeter does not run
// where its command line was written.
func ParseOptions(d Defaults, args []string) (Options, error) {
	o := Options{ConfigPath: config.GreeterConfigPath}
	var sessions, xsessions sessionDirFlags
	value := func(i *int, flag, need string) (string, error) {
		if *i+1 >= len(args) {
			return "", UsageError{flag + " requires " + need}
		}
		*i++
		return args[*i], nil
	}
	absolute := func(i *int, flag, need string) (string, error) {
		v, err := value(i, flag, need)
		if err == nil && !filepath.IsAbs(v) {
			err = UsageError{fmt.Sprintf("%s requires an absolute path, not %q", flag, v)}
		}
		return v, err
	}
	for i := 0; i < len(args); i++ {
		var err error
		var v string
		switch args[i] {
		case commandFlag:
			o.Command = append(o.Command, args[i+1:]...)
			i = len(args)
		case configFlag:
			v, err = absolute(&i, configFlag, "a path")
			o.ConfigPath = v
		case sessionsFlag:
			v, err = absolute(&i, sessionsFlag, "a DIR")
			sessions.dirs = append(sessions.dirs, v)
		case noSessionsFlag:
			sessions.none = true
		case xSessionsFlag:
			v, err = absolute(&i, xSessionsFlag, "a DIR")
			xsessions.dirs = append(xsessions.dirs, v)
		case noXSessionsFlag:
			xsessions.none = true
		case stateFlag:
			v, err = absolute(&i, stateFlag, "a path")
			o.StatePath = v
		case handOverFlag:
			o.HandOver = true
		case envFlag:
			v, err = value(&i, envFlag, "KEY=VAL")
			if err == nil && !strings.Contains(v, "=") {
				err = UsageError{envFlag + " value must be KEY=VAL"}
			}
			o.Env = append(o.Env, v)
		default:
			err = UsageError{"unexpected argument: " + args[i]}
		}
		if err != nil {
			return Options{}, err
		}
	}
	var err error
	if o.SessionDirs, err = sessions.resolved(sessionsFlag, noSessionsFlag, d.sessionDirs(waylandSessionsDir)); err != nil {
		return Options{}, err
	}
	if o.XSessionDirs, err = xsessions.resolved(xSessionsFlag, noXSessionsFlag, d.sessionDirs(xSessionsDir)); err != nil {
		return Options{}, err
	}
	if o.StatePath == "" {
		if d.stateErr != nil {
			return Options{}, d.stateErr
		}
		o.StatePath = d.statePath
	}
	return o, nil
}

// Args is the command line that parses back to o under any Defaults:
// it says everything, so a greeter started with it resolves nothing
// from its own environment.
func (o Options) Args() []string {
	args := make([]string, 0, 7+2*(len(o.SessionDirs)+len(o.XSessionDirs)+len(o.Env))+len(o.Command))
	args = append(args, configFlag, o.ConfigPath, stateFlag, o.StatePath)
	args = appendSessionDirs(args, sessionsFlag, noSessionsFlag, o.SessionDirs)
	args = appendSessionDirs(args, xSessionsFlag, noXSessionsFlag, o.XSessionDirs)
	for _, kv := range o.Env {
		args = append(args, envFlag, kv)
	}
	if o.HandOver {
		args = append(args, handOverFlag)
	}
	if len(o.Command) > 0 {
		args = append(append(args, commandFlag), o.Command...)
	}
	return args
}

func appendSessionDirs(args []string, dirFlag, noneFlag string, dirs []string) []string {
	if len(dirs) == 0 {
		return append(args, noneFlag)
	}
	for _, dir := range dirs {
		args = append(args, dirFlag, dir)
	}
	return args
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
