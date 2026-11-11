// Command lys-login is the login screen: one screen in two modes.
//
//	lys-login greet [--config PATH] [--sessions DIR]... [--no-sessions]
//	                [--xsessions DIR]... [--no-xsessions] [--state PATH]
//	                [--env KEY=VAL]... [--hand-over] [-- <session argv...>]
//	lys-login lock
//	lys-login daemon --vt N --service NAME --greeter-user NAME
//	                 --compositor PATH [--runtime-dir DIR] [--splash PATH]
//	                 [-- <greet args...>]
//	lys-login apply-config [--config PATH] <STAGED.yaml>
//	lys-login record-cursor
//	lys-login switch-user
//	lys-login upgrade [-- <daemon args...>]
//
// greet is the greetd greeter: the credential prompt over a session
// picker and the login users, run as the single client of a kiosk
// compositor; on success greetd starts the chosen session and replaces
// this process. lock locks the session with ext-session-lock-v1 and
// exits once it unlocks; the shell starts it for its lock triggers.
// daemon is the display manager in place of greetd and cage: run as root
// on a VT, it shows the greeter in a lys-wm kiosk and starts the session
// a user logs in to (its workers are `lys-login worker`, started by it).
// apply-config changes the login screen: run by the user logged in
// through daemon it hands the change to the daemon, run as root it
// writes it itself. record-cursor hands the cursor of the session it
// runs in (XCURSOR_THEME, XCURSOR_SIZE) to daemon, which keeps it for
// the next greeter. switch-user, run in a session started through daemon,
// locks the sessions and shows a greeter on a free VT for another user,
// whose own session, if it runs, the greeter switches to without a
// password. upgrade, run as root from the new build, moves the
// running daemon to that build in place: its sessions keep running on the
// workers they have, and a failure leaves the old daemon running and says
// why.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/shell/lockclient"
	"github.com/stubbedev/lys-lib/xdg"

	"github.com/stubbedev/lys-login/dmsocket"
	"github.com/stubbedev/lys-login/greeterconfig"
	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/cursorrecord"
	"github.com/stubbedev/lys-login/internal/dm"
	"github.com/stubbedev/lys-login/internal/greeter"
	"github.com/stubbedev/lys-login/internal/keyboard"
	"github.com/stubbedev/lys-login/internal/lock"
)

const usage = "usage: lys-login greet [options] [-- <session argv...>] | lys-login lock | lys-login daemon [options] [-- <greet args...>] | lys-login apply-config [--config PATH] <STAGED.yaml> | lys-login record-cursor | lys-login switch-user | lys-login upgrade [-- <daemon args...>]"

func init() { runtime.LockOSThread() }

func releaseMainThreadUnlessWorker(mode string) {
	if mode != "worker" {
		runtime.UnlockOSThread()
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	releaseMainThreadUnlessWorker(os.Args[1])
	args := os.Args[2:]
	switch os.Args[1] {
	case "greet":
		os.Exit(greet(args))
	case "lock":
		os.Exit(lockSession(args))
	case "daemon":
		os.Exit(daemon(args))
	case dm.ResumeMode:
		os.Exit(daemonResume(args))
	case "upgrade":
		os.Exit(upgrade(args))
	case "worker":
		os.Exit(worker(args))
	case "vault-unlock":
		os.Exit(vaultUnlock(args))
	case "apply-config":
		os.Exit(applyConfig(args))
	case "record-cursor":
		os.Exit(recordCursor(args))
	case "switch-user":
		os.Exit(switchUser(args))
	default:
		fmt.Fprintf(os.Stderr, "lys-login: unknown mode %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
}

func greet(args []string) int {
	opts, err := greeter.ParseOptions(greeter.UserDefaults(xdg.OS), args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if err := greeter.ApplyCacheDir(opts.StatePath); err != nil {
		fmt.Fprintln(os.Stderr, "lys-login greet:", err)
		return 1
	}
	cfg, err := config.LoadGreeter(opts.ConfigPath, config.StderrDiagnostics)
	if err != nil {
		log.Printf("greeter: config load failed; using defaults: %v", err)
	}

	sessions, err := opts.Sessions()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login greet:", err)
		return 2
	}

	err = greeter.Run(greeter.Init{
		Config:      cfg,
		Sessions:    sessions,
		Users:       greeter.LoadUsers(),
		LastSession: greeter.LoadLast(opts.StatePath),
		LastUser:    greeter.LoadLast(greeter.LastUserPath(opts.StatePath)),
		Layouts:     greeter.LoadLayouts(opts.StatePath),
		A11y:        loadAccessibility(opts.StatePath),
		StatePath:   opts.StatePath,
		SessionEnv:  opts.Env,
		OnPresented: dm.TakeScreenFromSplash(os.Getenv(dm.RuntimeDirEnv)),
		HandOver:    opts.HandOver,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login greet:", err)
		return 1
	}
	return 0
}

func lockSession(args []string) int {
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "lys-login lock: unexpected argument %q\n", args[0])
		return 2
	}
	svc, err := config.Open()
	if err != nil {
		log.Printf("lock: config: %v", err)
	}
	cfg := config.Defaults()
	if svc != nil {
		defer svc.Close()
		cfg = svc.Config()
	}
	var vaultHelper []string
	if exe, err := self(); err != nil {
		log.Printf("lock: vault: %v; not unlocking it with the session", err)
	} else {
		vaultHelper = []string{exe, "vault-unlock"}
	}
	switch err := lock.Run(cfg, os.Stdout, vaultHelper); {
	case err == nil:
		return 0
	case errors.Is(err, lock.ErrHeld):
		log.Print(err)
		return lockclient.ExitHeld
	case errors.Is(err, lock.ErrRefused):
		log.Print(err)
		return lockclient.ExitRefused
	default:
		fmt.Fprintln(os.Stderr, "lys-login lock:", err)
		return 1
	}
}

func vaultUnlock(args []string) int {
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "lys-login vault-unlock: unexpected argument %q\n", args[0])
		return 2
	}
	if err := lock.UnlockVault(os.Stdin); err != nil {
		log.Printf("lock: vault: %v", err)
		return 1
	}
	return 0
}

// applyConfig is `lys-login apply-config [--config PATH] <STAGED.yaml>`:
// it stages the login keys of STAGED, and the background image an image
// background names, with the caller's rights, and puts them in the
// runtime.yaml beside the greeter's system config. Root writes them
// itself (into PATH's directory); anyone else hands them to the display
// manager, which takes them from the user whose session it runs.
func applyConfig(args []string) int {
	fs := flag.NewFlagSet("lys-login apply-config", flag.ContinueOnError)
	configPath := fs.String("config", "", "as root, the greeter's system config (default "+config.GreeterConfigPath+")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: lys-login apply-config [--config PATH] <STAGED.yaml>")
		return 2
	}
	asRoot := os.Geteuid() == 0
	if *configPath != "" && !asRoot {
		fmt.Fprintln(os.Stderr, "lys-login apply-config: --config is root's: a user's change goes to the display manager, which writes the config it shows")
		return 2
	}
	c, err := greeterconfig.Stage(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login apply-config:", err)
		return 1
	}
	var dest string
	if asRoot {
		if *configPath == "" {
			*configPath = config.GreeterConfigPath
		}
		dest, err = writeConfig(c, *configPath)
	} else {
		dest, err = dm.ApplySocket.Send(dmsocket.DefaultRuntimeDir, c)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login apply-config:", err)
		return 1
	}
	fmt.Println("wrote", dest)
	return 0
}

func writeConfig(c greeterconfig.Change, configPath string) (string, error) {
	checked, err := c.Check()
	if err != nil {
		return "", err
	}
	return greeterconfig.Write(checked, configPath)
}

// recordCursor is `lys-login record-cursor`: run in a session the
// display manager started, it hands the session's cursor to it, which
// records it for the greeter. A session that names no cursor records
// nothing.
func recordCursor(args []string) int {
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "lys-login record-cursor: unexpected argument %q\n", args[0])
		return 2
	}
	c, ok, err := cursorrecord.FromEnv(xdg.OS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login record-cursor:", err)
		return 1
	}
	if !ok {
		return 0
	}
	dest, err := dm.CursorSocket.Send(dmsocket.DefaultRuntimeDir, c)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login record-cursor:", err)
		return 1
	}
	fmt.Println("wrote", dest)
	return 0
}

func switchUser(args []string) int {
	if len(args) != 0 {
		fmt.Fprintf(os.Stderr, "lys-login switch-user: unexpected argument %q\n", args[0])
		return 2
	}
	if _, err := dm.SwitchSocket.Send(dmsocket.DefaultRuntimeDir, dm.SwitchRequest{}); err != nil {
		fmt.Fprintln(os.Stderr, "lys-login switch-user:", err)
		return 1
	}
	return 0
}

// workerFlags are the flags every worker takes.
func workerFlags(fs *flag.FlagSet) *dm.WorkerOptions {
	o := &dm.WorkerOptions{}
	fs.StringVar(&o.Service, "service", "lys-login", "PAM service")
	fs.IntVar(&o.VT, "vt", 1, "virtual terminal")
	return o
}

func parseDaemonArgs(args []string) (dm.Options, int) {
	fs := flag.NewFlagSet("lys-login daemon", flag.ContinueOnError)
	wo := workerFlags(fs)
	greeterUser := fs.String("greeter-user", "lys-greeter", "account the greeter runs as")
	compositor := fs.String("compositor", "lys-wm", "the greeter's compositor")
	runtimeDir := fs.String("runtime-dir", dmsocket.DefaultRuntimeDir, "directory for the greeter's socket and the display manager's")
	splash := fs.String("splash", "", "lys-splash, quit with --retain-splash once the greeter's first frame is presented")
	autologinUser := fs.String("autologin", "", "log this user in once per boot, before the first greeter")
	autologinSession := fs.String("autologin-session", "", "the session id the autologin starts, else the last session or the first offered")
	if err := fs.Parse(args); err != nil {
		return dm.Options{}, 2
	}
	greet, err := greeter.ParseOptions(greeter.SystemDefaults(xdg.OS), fs.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login daemon: the greeter's arguments:", err)
		return dm.Options{}, 2
	}
	sessions, err := greet.Sessions()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login daemon: the greeter would offer nothing to log into:", err)
		return dm.Options{}, 2
	}
	autologin, err := autologinTarget(*autologinUser, *autologinSession, sessions, greeter.LoadLast(greet.StatePath), greet.Env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login daemon:", err)
		return dm.Options{}, 2
	}
	if wo.Self, err = self(); err != nil {
		fmt.Fprintln(os.Stderr, "lys-login daemon:", err)
		return dm.Options{}, 1
	}
	return dm.Options{
		WorkerOptions: *wo, GreeterUser: *greeterUser,
		Compositor: []string{*compositor}, Greeter: greet, RuntimeDir: *runtimeDir,
		CursorDir: cursorrecord.Dir,
		Keyboard:  keyboard.System,
		Splash:    *splash, Args: args, Autologin: autologin,
	}, 0
}

func autologinTarget(user, session string, sessions []greeter.Session, last string, env []string) (*dm.Autologin, error) {
	if user == "" {
		if session != "" {
			return nil, fmt.Errorf("--autologin-session %q without --autologin names nobody to log in", session)
		}
		return nil, nil
	}
	want := session
	if want == "" {
		want = last
	}
	picked := sessions[0]
	if i := slices.IndexFunc(sessions, func(s greeter.Session) bool { return s.ID == want }); i >= 0 {
		picked = sessions[i]
	} else if session != "" {
		return nil, fmt.Errorf("--autologin-session %q is none of the sessions offered", session)
	}
	return &dm.Autologin{User: user, Session: picked.Started(env)}, nil
}

func daemon(args []string) int {
	o, code := parseDaemonArgs(args)
	if code != 0 {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), dm.StopSignals...)
	defer stop()
	if err := dm.Run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "lys-login daemon:", err)
		return 1
	}
	return 0
}

func daemonResume(args []string) int {
	fs := flag.NewFlagSet("lys-login "+dm.ResumeMode, flag.ContinueOnError)
	check := fs.Bool(dm.ResumeCheckFlag, false, "only check that the handover can be resumed")
	stateFD := fs.Int(dm.ResumeStateFlag, -1, "the descriptor of the handed-over state")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o, code := parseDaemonArgs(fs.Args())
	if code != 0 {
		return code
	}
	if *check {
		if err := auth.CheckLibrary(); err != nil {
			fmt.Fprintln(os.Stderr, "lys-login "+dm.ResumeMode+":", err)
			return 1
		}
		if err := dm.CheckResume(*stateFD, o, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "lys-login "+dm.ResumeMode+":", err)
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), dm.StopSignals...)
	defer stop()
	if err := dm.Resume(ctx, o, *stateFD); err != nil {
		fmt.Fprintln(os.Stderr, "lys-login "+dm.ResumeMode+":", err)
		return 1
	}
	return 0
}

func upgrade(args []string) int {
	fs := flag.NewFlagSet("lys-login upgrade", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o, code := parseDaemonArgs(fs.Args())
	if code != 0 {
		return code
	}
	running, err := dm.UpgradeSocket.Send(o.RuntimeDir, dm.UpgradeRequest{Exec: o.Self, Args: o.Args})
	if err != nil {
		fmt.Fprintln(os.Stderr, "lys-login upgrade:", err)
		return 1
	}
	fmt.Println("the display manager now runs", running)
	return 0
}

func worker(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "lys-login worker: greeter or session")
		return 2
	}
	kind := args[0]
	fs := flag.NewFlagSet("lys-login worker "+kind, flag.ContinueOnError)
	wo := workerFlags(fs)
	user := fs.String("user", "lys-greeter", "the greeter's account")
	var env multiFlag
	fs.Var(&env, "env", "KEY=value for the greeter's compositor (repeatable)")
	fs.StringVar(&wo.RuntimeDir, "runtime-dir", dmsocket.DefaultRuntimeDir, "directory for the lock a session holds while it runs")
	fs.BoolVar(&wo.Autologin, "autologin", false, "log the user in with the autologin PAM service, which asks nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	var err error
	if wo.Self, err = self(); err != nil {
		fmt.Fprintf(os.Stderr, "lys-login worker %s: %v\n", kind, err)
		return 1
	}
	switch kind {
	case "greeter":
		if fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "lys-login worker greeter: no compositor command after --")
			return 2
		}
		err = dm.RunGreeter(*wo, *user, fs.Args(), env)
	case "session":
		ch := os.NewFile(3, "dm")
		if ch == nil {
			fmt.Fprintln(os.Stderr, "lys-login worker session: no channel on fd 3")
			return 2
		}
		err = dm.RunSession(*wo, ch)
	default:
		fmt.Fprintf(os.Stderr, "lys-login worker: unknown kind %q\n", kind)
		return 2
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "lys-login worker %s: %v\n", kind, err)
		return 1
	}
	return 0
}

// multiFlag collects a repeated flag.
type multiFlag []string

func (m *multiFlag) String() string     { return fmt.Sprint(*m) }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// self is the lys-login the workers and the greeter run: the program of
// that name beside the running one (a package's wrapper, which sets the
// library path), else the running one.
func self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find lys-login: %w", err)
	}
	sibling := filepath.Join(filepath.Dir(exe), "lys-login")
	if _, err := os.Stat(sibling); err == nil {
		return sibling, nil
	}
	return exe, nil
}

func loadAccessibility(statePath string) greeter.Accessibility {
	a, err := greeter.LoadAccessibility(statePath)
	if err != nil {
		log.Printf("greeter: starting with every accessibility option off: %v", err)
	}
	return a
}
