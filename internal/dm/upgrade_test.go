package dm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/sockdir"

	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/cursorrecord"
)

const (
	fakeDirEnv        = "LYS_FAKE_DIR"
	fakeRuntimeEnv    = "LYS_FAKE_RUNTIME"
	fakeGenerationEnv = "LYS_FAKE_GENERATION"
	fakeBreakEnv      = "LYS_FAKE_BREAK_RESUME"
	fakePanicEnv      = "LYS_FAKE_PANIC_RESUME"
	fakeRootOnlyEnv   = "LYS_FAKE_ROOT_ONLY"
	fakeDaemonMode    = "fake-daemon"
	burstSize         = 3000
	stepDeadline      = 30 * time.Second
)

func openDescriptors() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		panic(err)
	}
	return len(entries)
}

func runFakeMode() (int, bool) {
	if os.Getenv(fakeDirEnv) == "" || len(os.Args) < 2 {
		return 0, false
	}
	switch os.Args[1] {
	case fakeDaemonMode, ResumeMode:
		return fakeDaemon(), true
	case "worker":
		if os.Args[2] == "greeter" {
			return fakeGreeterWorker(), true
		}
		return fakeLongSessionWorker(), true
	}
	return 0, false
}

func appendLine(name, line string) {
	f, err := os.OpenFile(filepath.Join(os.Getenv(fakeDirEnv), name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		panic(err)
	}
}

type fileCard struct{ f *os.File }

func (c fileCard) FD() int           { return int(c.f.Fd()) }
func (c fileCard) DropMaster() error { return nil }
func (c fileCard) Close() error      { return c.f.Close() }

func fakeConsole(dir string) console {
	return console{
		activate: func(int) error {
			appendLine("activations", "vt")
			return nil
		},
		showText: func(_ int, on bool) error {
			appendLine("console-modes", strconv.FormatBool(on))
			return nil
		},
		cards: &heldCards{
			glob: filepath.Join(dir, "card[0-9]*"),
			open: func(path string) (openCard, error) {
				appendLine("card-opens", path)
				f, err := os.Open(path)
				if err != nil {
					return nil, err
				}
				return fileCard{f}, nil
			},
			fromFD: func(fd int, path string) openCard {
				return fileCard{os.NewFile(uintptr(fd), path)}
			},
			cards: map[string]openCard{},
		},
		stop: stopWorker,
	}
}

func fakeOptions(dir, runtime string) Options {
	self, err := os.Executable()
	if err != nil {
		panic(err)
	}
	u, err := user.Current()
	if err != nil {
		panic(err)
	}
	return Options{
		Service:       "lys-login",
		VT:            7,
		Self:          self,
		GreeterUser:   u.Username,
		Compositor:    []string{"sh"},
		RuntimeDir:    runtime,
		GreeterConfig: filepath.Join(dir, "config.yaml"),
		GreeterState:  filepath.Join(dir, "last-session"),
		CursorDir:     filepath.Join(dir, "cursor"),
		Args:          []string{"fake"},
	}
}

func fakeDaemon() int {
	dir, runtime := os.Getenv(fakeDirEnv), os.Getenv(fakeRuntimeEnv)
	o := fakeOptions(dir, runtime)
	may := func(uint32) bool { return true }
	if os.Getenv(fakeRootOnlyEnv) != "" {
		may = rootOnly
	}
	ctx, stop := signal.NotifyContext(context.Background(), StopSignals...)
	defer stop()
	c := fakeConsole(dir)
	if os.Getenv(fakeLiveSessionEnv) != "" {
		held, err := holdSessionLock(runtime)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fake daemon:", err)
			return 1
		}
		release := make(chan os.Signal, 1)
		signal.Notify(release, syscall.SIGUSR1)
		go func() {
			<-release
			_ = held.Close()
		}()
	}
	var err error
	if os.Args[1] == ResumeMode {
		err = fakeResume(ctx, o, c, may)
	} else {
		appendLine("daemon-pid", strconv.Itoa(os.Getpid()))
		err = runDaemon(ctx, o, c, may, nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake daemon:", err)
		return 1
	}
	return 0
}

func fakeResume(ctx context.Context, o Options, c console, may func(uint32) bool) error {
	args := os.Args[2:]
	check := args[0] == "--"+ResumeCheckFlag
	if check {
		args = args[1:]
	}
	if args[0] != "--"+ResumeStateFlag {
		return fmt.Errorf("the arguments %q name no state", os.Args)
	}
	fd, err := strconv.Atoi(args[1])
	if err != nil {
		return err
	}
	if check {
		return CheckResume(fd, o, os.Stdout)
	}
	if os.Getenv(fakeBreakEnv) != "" {
		o.RuntimeDir = filepath.Join(o.RuntimeDir, "elsewhere")
	}
	if os.Getenv(fakePanicEnv) != "" {
		c.cards.fromFD = func(int, string) openCard { panic("the new build trips over a card") }
	}
	return resume(ctx, o, c, may, fd)
}

func fakeGreeterWorker() int {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	appendLine("greeters", fmt.Sprintf("%d %s", os.Getpid(), os.Getenv(fakeGenerationEnv)))
	orphaned := parentGone()
	select {
	case <-stop:
	case <-orphaned:
	}
	return 0
}

func parentGone() <-chan struct{} {
	gone := make(chan struct{})
	data, err := os.ReadFile(filepath.Join(os.Getenv(fakeDirEnv), "daemon-pid"))
	if err != nil {
		panic(err)
	}
	parent, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		panic(err)
	}
	go func() {
		for os.Getppid() == parent {
			time.Sleep(50 * time.Millisecond)
		}
		close(gone)
	}()
	return gone
}

func fakeLongSessionWorker() int {
	ch := newChannel(os.NewFile(3, "dm"))
	var req request
	if err := ch.recv(&req); err != nil || req.Op != opAuth {
		return 1
	}
	if err := ch.send(event{Kind: evPrompt, Prompt: &auth.Prompt{Kind: auth.PromptSecret, Text: "Password:"}}); err != nil {
		return 1
	}
	if err := ch.recv(&req); err != nil || req.Op != opAnswer || req.Answer == nil || *req.Answer != "pw" {
		_ = ch.send(event{Kind: evFailed, Text: "Authentication failure"})
		return 0
	}
	if err := ch.send(event{Kind: evAuthenticated}); err != nil {
		return 1
	}
	if err := ch.recv(&req); err != nil || req.Op != opStart {
		return 0
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGUSR1)
	appendLine("workers", fmt.Sprintf("%d %s", os.Getpid(), os.Getenv(fakeGenerationEnv)))
	appendLine("worker-descriptors", strconv.Itoa(openDescriptors()))
	if err := ch.send(event{Kind: evStarted, UID: uint32(os.Getuid())}); err != nil {
		return 1
	}
	orphaned := parentGone()
	for running := true; running; {
		var sig os.Signal
		select {
		case sig = <-signals:
		case <-orphaned:
			return 0
		}
		if sig == syscall.SIGTERM {
			running = false
			continue
		}
		for i := range burstSize {
			if err := ch.send(event{Kind: evKeyringLocked, Text: fmt.Sprintf("burst %d", i)}); err != nil {
				return 1
			}
		}
	}
	if err := ch.send(event{Kind: evEnded, Text: "exited"}); err != nil {
		return 1
	}
	return 0
}

type fakeDM struct {
	t       *testing.T
	dir     string
	runtime string
	pid     int
	done    chan error
}

func startFakeDM(t *testing.T, env ...string) *fakeDM {
	t.Helper()
	runtime := sockdir.Dir(t)
	dir, err := os.MkdirTemp("", "lys-fake-dm-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "card0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.Create(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	attr := &os.ProcAttr{
		Env:   append(os.Environ(), fakeDirEnv+"="+dir, fakeRuntimeEnv+"="+runtime, fakeGenerationEnv+"=1"),
		Files: []*os.File{nil, log, log},
	}
	attr.Env = append(attr.Env, env...)
	proc, err := os.StartProcess(self, []string{self, fakeDaemonMode}, attr)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDM{t: t, dir: dir, runtime: runtime, pid: proc.Pid, done: make(chan error, 1)}
	go func() {
		state, err := proc.Wait()
		if err == nil && !state.Success() && state.ExitCode() != -1 {
			err = fmt.Errorf("the fake daemon exited: %s", state)
		}
		d.done <- err
	}()
	t.Cleanup(func() {
		if t.Failed() {
			d.dump()
		}
		_ = proc.Kill()
		<-d.done
		d.done <- nil
		d.remove()
	})
	return d
}

func (d *fakeDM) remove() {
	deadline := time.Now().Add(stepDeadline)
	for {
		for _, name := range []string{"workers", "greeters"} {
			for _, p := range d.pids(name) {
				_ = syscall.Kill(p, syscall.SIGKILL)
			}
		}
		err := os.RemoveAll(d.dir)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			d.t.Errorf("remove %s: %v", d.dir, err)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (d *fakeDM) dump() {
	for _, name := range []string{"daemon.log", "greeters", "workers", "card-opens"} {
		data, err := os.ReadFile(filepath.Join(d.dir, name))
		d.t.Logf("--- %s (%v)\n%s", name, err, tail(data))
	}
}

func tail(data []byte) []byte {
	if len(data) > 8192 {
		return data[len(data)-8192:]
	}
	return data
}

func (d *fakeDM) lines(name string) []string {
	data, err := os.ReadFile(filepath.Join(d.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		d.t.Fatal(err)
	}
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func (d *fakeDM) pids(name string) []int {
	var pids []int
	for _, line := range d.lines(name) {
		field, _, _ := strings.Cut(line, " ")
		if p, err := strconv.Atoi(field); err == nil {
			pids = append(pids, p)
		}
	}
	return pids
}

func (d *fakeDM) await(what string, ok func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(stepDeadline)
	for !ok() {
		select {
		case err := <-d.done:
			d.done <- err
			d.t.Fatalf("waiting for %s, the fake daemon ended: %v", what, err)
		default:
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("no %s in %s", what, stepDeadline)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (d *fakeDM) awaitGreeter(n int) (pid int, generation string) {
	d.t.Helper()
	d.await(fmt.Sprintf("greeter %d", n), func() bool { return len(d.lines("greeters")) >= n })
	pid, generation, ok := cutPid(d.lines("greeters")[n-1])
	if !ok {
		d.t.Fatalf("greeter line %d", n)
	}
	return pid, generation
}

func cutPid(line string) (int, string, bool) {
	field, generation, ok := strings.Cut(line, " ")
	if !ok {
		return 0, "", false
	}
	pid, err := strconv.Atoi(field)
	return pid, generation, err == nil
}

func (d *fakeDM) dial() net.Conn {
	d.t.Helper()
	var conn net.Conn
	d.await("the greeter's socket", func() bool {
		c, err := net.Dial("unix", filepath.Join(d.runtime, "greeter.sock"))
		if err != nil {
			return false
		}
		conn = c
		return true
	})
	d.t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func (d *fakeDM) login() (worker int, generation string) {
	d.t.Helper()
	before := len(d.lines("workers"))
	g := auth.NewGreetd(d.dial(), auth.Start{Session: func() auth.Session {
		return auth.Session{Cmd: []string{"fake-session"}, Env: []string{"XDG_SESSION_TYPE=wayland"}}
	}})
	if err := g.Run("alice", func(auth.Prompt) (string, bool) { return "pw", true }); err != nil {
		d.t.Fatalf("login: %v", err)
	}
	d.await("the session's worker", func() bool { return len(d.lines("workers")) > before })
	d.await("the daemon tracking the session", d.tracks)
	pid, generation, ok := cutPid(d.lines("workers")[before])
	if !ok {
		d.t.Fatal("the worker's line")
	}
	return pid, generation
}

func (d *fakeDM) tracks() bool {
	_, err := CursorSocket.Send(d.runtime, cursorrecord.Cursor{Theme: "x", Size: 24})
	return err == nil
}

func (d *fakeDM) upgrade(exec string) (string, error) {
	return UpgradeSocket.Send(d.runtime, UpgradeRequest{Exec: exec, Args: []string{"fake"}})
}

func (d *fakeDM) cmdline() string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", d.pid))
	if err != nil {
		d.t.Fatal(err)
	}
	return string(bytes.ReplaceAll(data, []byte{0}, []byte{' '}))
}

func (d *fakeDM) descriptors() int {
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", d.pid))
	if err != nil {
		d.t.Fatal(err)
	}
	return len(entries)
}

func (d *fakeDM) logged() string {
	data, err := os.ReadFile(filepath.Join(d.dir, "daemon.log"))
	if err != nil {
		d.t.Fatal(err)
	}
	return string(data)
}

func (d *fakeDM) endSession(worker int) {
	d.t.Helper()
	if err := syscall.Kill(worker, syscall.SIGTERM); err != nil {
		d.t.Fatalf("end the session of worker %d: %v", worker, err)
	}
	d.await("the daemon reaping the worker", func() bool {
		_, err := os.Stat(fmt.Sprintf("/proc/%d", worker))
		return errors.Is(err, os.ErrNotExist)
	})
	d.await("the daemon letting go of the session", func() bool { return !d.tracks() })
}

func wrapper(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "next-lys-login")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nextBuild(t *testing.T, env ...string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return wrapper(t, strings.Join(env, " ")+" exec "+self+` "$@"`)
}

func TestAnUpgradeKeepsTheSessionAndTracksIt(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	worker, generation := d.login()
	if generation != "1" {
		t.Fatalf("the first worker runs generation %q", generation)
	}
	if err := syscall.Kill(worker, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	baseline := 0
	for _, next := range []string{"2", "3", "4"} {
		path := nextBuild(t, fakeGenerationEnv+"="+next)
		if _, err := d.upgrade(path); err != nil {
			t.Fatalf("upgrade to generation %s: %v", next, err)
		}
		if got := d.cmdline(); !strings.Contains(got, ResumeMode) {
			t.Fatalf("after the upgrade pid %d runs %q", d.pid, got)
		}
		if !d.tracks() {
			t.Fatal("the upgraded daemon does not track the session")
		}
		if baseline == 0 {
			baseline = d.descriptors()
			continue
		}
		d.await("the descriptors of an upgrade closed", func() bool { return d.descriptors() <= baseline })
	}
	if got := d.lines("card-opens"); len(got) != 1 {
		t.Errorf("the cards were opened %d times: %v; want held across the upgrades", len(got), got)
	}
	if got := d.lines("activations"); len(got) != 1 {
		t.Errorf("the VT was activated %d times under the session: %v", len(got), got)
	}
	d.endSession(worker)
	d.await("all of the burst", func() bool { return strings.Contains(d.logged(), fmt.Sprintf("burst %d\n", burstSize-1)) })
	assertBurstOnce(t, d.logged())
	if pid, generation := d.awaitGreeter(2); generation != "4" || pid == worker {
		t.Errorf("the next greeter is pid %d of generation %q, want the new generation", pid, generation)
	}
	next, generation := d.login()
	if generation != "4" {
		t.Errorf("the next login's worker is generation %q", generation)
	}
	if got := d.lines("worker-descriptors"); len(got) != 2 || got[0] != got[1] {
		t.Errorf("descriptors of the workers before and after the upgrades: %v; the handover leaked into a child", got)
	}
	d.endSession(next)
}

func assertBurstOnce(t *testing.T, log string) {
	t.Helper()
	want := 0
	sc := bufio.NewScanner(strings.NewReader(log))
	for sc.Scan() {
		_, number, ok := strings.Cut(sc.Text(), "burst ")
		if !ok {
			continue
		}
		got, err := strconv.Atoi(number)
		if err != nil || got != want {
			t.Fatalf("event %d of the burst is %q, an event was lost or repeated", want, sc.Text())
		}
		want++
	}
	if want != burstSize {
		t.Fatalf("%d events of the burst of %d arrived", want, burstSize)
	}
}

func TestAPreflightRefusalLeavesTheOldDaemonAndSaysWhy(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	worker, _ := d.login()
	cmdline := d.cmdline()
	for name, tc := range map[string]struct{ body, says string }{
		"exits with its reason":      {"echo this build is broken >&2; exit 7", "this build is broken"},
		"confirms nothing":           {"exit 0", "did not confirm"},
		"does not know the handover": {"echo unknown mode daemon-resume >&2; exit 2", "unknown mode"},
	} {
		_, err := d.upgrade(wrapper(t, tc.body))
		if err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: upgrade = %v, want it to say %q", name, err, tc.says)
		}
		if got := d.cmdline(); got != cmdline {
			t.Errorf("%s: the daemon now runs %q", name, got)
		}
		if !d.tracks() {
			t.Errorf("%s: the daemon stopped tracking the session", name)
		}
	}
	if _, err := d.upgrade(filepath.Join(d.dir, "no-such-build")); err == nil || !strings.Contains(err.Error(), "no-such-build") {
		t.Errorf("a missing build: upgrade = %v, want it named", err)
	}
	if _, err := d.upgrade("relative/lys-login"); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Errorf("a relative build: upgrade = %v", err)
	}
	if got := d.lines("card-opens"); len(got) != 1 {
		t.Errorf("the cards were opened %d times", len(got))
	}
	if _, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2")); err != nil {
		t.Fatalf("a good upgrade after the refusals: %v", err)
	}
	d.endSession(worker)
	if _, generation := d.awaitGreeter(2); generation != "2" {
		t.Errorf("the next greeter is generation %q", generation)
	}
}

func TestAResumeThatFailsGoesBackToTheOldDaemon(t *testing.T) {
	for name, tc := range map[string]struct{ env, says string }{
		"refuses the handover": {fakeBreakEnv + "=1", "this daemon is to run in"},
		"panics":               {fakePanicEnv + "=1", "panicked"},
	} {
		t.Run(name, func(t *testing.T) {
			d := startFakeDM(t)
			d.awaitGreeter(1)
			worker, _ := d.login()
			if err := syscall.Kill(worker, syscall.SIGUSR1); err != nil {
				t.Fatal(err)
			}
			_, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2", tc.env))
			if err == nil || !strings.Contains(err.Error(), "could not resume") || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("upgrade = %v, want the failed resume and %q", err, tc.says)
			}
			if !d.tracks() {
				t.Fatal("the old daemon came back without the session")
			}
			if got := d.lines("card-opens"); len(got) != 1 {
				t.Errorf("the cards were opened %d times across the rollback", len(got))
			}
			if _, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=3")); err != nil {
				t.Errorf("an upgrade after the rollback: %v", err)
			}
			d.endSession(worker)
			d.await("all of the burst", func() bool { return strings.Contains(d.logged(), fmt.Sprintf("burst %d\n", burstSize-1)) })
			assertBurstOnce(t, d.logged())
			if _, generation := d.awaitGreeter(2); generation != "3" {
				t.Errorf("after the rollback and a good upgrade the greeter is generation %q", generation)
			}
		})
	}
}

func TestARollbackLeavesTheOldGeneration(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	worker, _ := d.login()
	if _, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2", fakeBreakEnv+"=1")); err == nil {
		t.Fatal("the broken build was taken")
	}
	d.endSession(worker)
	if _, generation := d.awaitGreeter(2); generation != "1" {
		t.Errorf("after the rollback the greeter is generation %q, want the old one", generation)
	}
}

func TestAnUpgradeAtTheGreeterShowsTheNewGreeter(t *testing.T) {
	d := startFakeDM(t)
	first, _ := d.awaitGreeter(1)
	if _, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2")); err != nil {
		t.Fatal(err)
	}
	second, generation := d.awaitGreeter(2)
	if generation != "2" || second == first {
		t.Errorf("the greeter after the upgrade is pid %d of generation %q", second, generation)
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", first)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old greeter's worker is still there: %v", err)
	}
	if got := d.lines("card-opens"); len(got) != 1 {
		t.Errorf("the cards were opened %d times, want held across the upgrade", len(got))
	}
	worker, generation := d.login()
	if generation != "2" {
		t.Errorf("the worker after the upgrade is generation %q", generation)
	}
	d.endSession(worker)
}

func TestARefusedUpgradeLeavesTheGreeterUp(t *testing.T) {
	d := startFakeDM(t)
	first, _ := d.awaitGreeter(1)
	if _, err := d.upgrade(wrapper(t, "echo broken >&2; exit 1")); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("upgrade = %v", err)
	}
	if err := syscall.Kill(first, 0); err != nil {
		t.Errorf("the greeter's worker went away: %v", err)
	}
	if got := d.lines("greeters"); len(got) != 1 {
		t.Errorf("greeters %v, want the first still up", got)
	}
	worker, _ := d.login()
	d.endSession(worker)
}

func within[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(stepDeadline):
		t.Fatalf("no %s in %s", what, stepDeadline)
		panic("unreachable")
	}
}

func TestALoginUnderWayFinishesBeforeTheUpgrade(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	g := auth.NewGreetd(d.dial(), auth.Start{Session: func() auth.Session {
		return auth.Session{Cmd: []string{"fake-session"}, Env: []string{"XDG_SESSION_TYPE=wayland"}}
	}})
	upgraded := make(chan error, 1)
	logged := make(chan error, 1)
	go func() {
		logged <- g.Run("alice", func(auth.Prompt) (string, bool) {
			go func() {
				_, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2"))
				upgraded <- err
			}()
			for deadline := time.Now().Add(stepDeadline); time.Now().Before(deadline) && !strings.Contains(d.logged(), "dm: an upgrade waits for the logins under way"); {
				time.Sleep(5 * time.Millisecond)
			}
			return "pw", true
		})
	}()
	if err := within(t, "end of the login", logged); err != nil {
		t.Fatalf("the login under way failed: %v", err)
	}
	if err := within(t, "answer to the upgrade", upgraded); err != nil {
		t.Fatalf("the upgrade: %v", err)
	}
	if !strings.Contains(d.logged(), "dm: an upgrade waits for the logins under way") {
		t.Fatal("the upgrade did not wait for the login under way")
	}
	d.await("the worker", func() bool { return len(d.lines("workers")) == 1 })
	worker := d.pids("workers")[0]
	d.await("the session tracked", d.tracks)
	if got := d.lines("greeters"); len(got) != 1 {
		t.Errorf("greeters = %v, want the login to have won", got)
	}
	d.endSession(worker)
	if _, generation := d.awaitGreeter(2); generation != "2" {
		t.Errorf("the next greeter is generation %q", generation)
	}
}

func TestALoginThatStallsIsCancelledForTheUpgrade(t *testing.T) {
	d := startFakeDM(t)
	first, _ := d.awaitGreeter(1)
	conn := d.dial()
	g := auth.NewGreetd(conn, auth.Start{})
	prompted, release := make(chan struct{}), make(chan struct{})
	logged := make(chan error, 1)
	go func() {
		logged <- g.Run("alice", func(auth.Prompt) (string, bool) {
			close(prompted)
			<-release
			return "pw", true
		})
	}()
	within(t, "prompt", prompted)
	if _, err := d.upgrade(nextBuild(t, fakeGenerationEnv+"=2")); err != nil {
		t.Fatalf("the upgrade waited on a stalled login: %v", err)
	}
	close(release)
	if err := within(t, "end of the stalled login", logged); err == nil {
		t.Error("the stalled login went on after the upgrade")
	}
	if second, generation := d.awaitGreeter(2); second == first || generation != "2" {
		t.Errorf("the greeter is pid %d of generation %q", second, generation)
	}
}

func TestOnlyRootMayUpgrade(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may")
	}
	d := startFakeDM(t, fakeRootOnlyEnv+"=1")
	first, _ := d.awaitGreeter(1)
	if _, err := d.upgrade(nextBuild(t)); err == nil || !strings.Contains(err.Error(), "only root") {
		t.Fatalf("upgrade by uid %d = %v", os.Getuid(), err)
	}
	if err := syscall.Kill(first, 0); err != nil {
		t.Errorf("a refused upgrade took the greeter down: %v", err)
	}
}

func goodHandover() handover {
	uid := uint32(1000)
	return handover{
		Version:    handoverVersion,
		From:       "/nix/store/old/bin/lys-login",
		RuntimeDir: "/run/lys-login",
		Client:     3,
		Listeners:  handedListeners{Apply: 4, Cursor: 5, Splash: 6, Upgrade: 7},
		Cards:      []handedCard{{Path: "/dev/dri/card0", FD: 8}},
		Session:    &handedSession{WorkerPID: 42, Channel: 9, Protocol: workerProtocol, Unread: []byte("{\"kind\":"), UID: &uid, Cmd: []string{"niri"}},
		Rollback:   rollback{Exe: 10, Args: []string{"--vt", "1"}, Env: []string{"A=b"}},
	}
}

func TestHandoverRoundTrips(t *testing.T) {
	want := goodHandover()
	data, err := want.encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseHandover(data)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if !bytes.Equal(wantJSON, gotJSON) {
		t.Errorf("round trip =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
	noSession := goodHandover()
	noSession.Session = nil
	if _, err := noSession.encode(); err != nil {
		t.Errorf("no session: %v", err)
	}
}

func TestHandoverRefusesWhatItCannotTrust(t *testing.T) {
	mutate := func(fn func(*handover)) []byte {
		h := goodHandover()
		fn(&h)
		data, err := json.Marshal(h)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	good, err := json.Marshal(goodHandover())
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		data []byte
		says string
	}{
		"not JSON":         {[]byte("nope"), "handed-over state"},
		"a later version":  {mutate(func(h *handover) { h.Version = handoverVersion + 1 }), "this build reads version"},
		"no version":       {[]byte(`{"from":"/x"}`), "version 0"},
		"an unknown field": {append(good[:len(good)-1], []byte(`,"extra":1}`)...), "extra"},
		"data after":       {append(slices.Clone(good), []byte(` {}`)...), "handed-over state"},
		"relative program": {mutate(func(h *handover) { h.From = "lys-login" }), "absolute"},
		"no rollback args": {mutate(func(h *handover) { h.Rollback.Args = nil }), "roll back"},
		"bad rollback env": {mutate(func(h *handover) { h.Rollback.Env = []string{"A"} }), "KEY=value"},
		"relative runtime": {mutate(func(h *handover) { h.RuntimeDir = "run" }), "runtime directory"},
		"relative card":    {mutate(func(h *handover) { h.Cards[0].Path = "card0" }), "absolute"},
		"init as worker":   {mutate(func(h *handover) { h.Session.WorkerPID = 1 }), "pid 1"},
		"other protocol":   {mutate(func(h *handover) { h.Session.Protocol = workerProtocol + 1 }), "protocol"},
		"no session cmd":   {mutate(func(h *handover) { h.Session.Cmd = nil }), "no command"},
		"stdin as client":  {mutate(func(h *handover) { h.Client = 0 }), "below 3"},
		"a shared card fd": {mutate(func(h *handover) { h.Cards[0].FD = h.Client }), "both on descriptor"},
		"missing listener": {mutate(func(h *handover) { h.Listeners.Splash = 0 }), "splash.sock"},
		"a shared channel": {mutate(func(h *handover) { h.Session.Channel = h.Listeners.Apply }), "both on descriptor"},
		"rollback exe low": {mutate(func(h *handover) { h.Rollback.Exe = 2 }), "the old program"},
		"two cards one fd": {mutate(func(h *handover) { h.Cards = append(h.Cards, handedCard{"/dev/dri/card1", 8}) }), "both on descriptor"},
	} {
		if _, err := parseHandover(tc.data); err == nil || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v, want it to say %q", name, err, tc.says)
		}
	}
}

func TestChannelLosesNothingAcrossADeadline(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	go func() {
		_, _ = a.Write([]byte("{\"kind\":\"one\"}\n{\"kind\":\"two\"}\n{\"kind\":"))
	}()
	ch := newChannel(b)
	var ev event
	if err := ch.recv(&ev); err != nil || ev.Kind != "one" {
		t.Fatalf("recv = %v, %v", ev, err)
	}
	if err := ch.recv(&ev); err != nil || ev.Kind != "two" {
		t.Fatalf("recv = %v, %v", ev, err)
	}
	reading := make(chan error, 1)
	go func() { reading <- ch.recv(&ev) }()
	time.Sleep(10 * time.Millisecond)
	if err := b.SetReadDeadline(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := <-reading; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("recv past the deadline = %v", err)
	}
	unread := ch.unread()
	if string(unread) != "{\"kind\":" {
		t.Fatalf("unread = %q", unread)
	}
	if err := b.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	next := resumeChannel(b, append(unread, "\"three\"}\n{\"kind\":\"four\"}\n{\"ki"...))
	if err := next.recv(&ev); err != nil || ev.Kind != "three" {
		t.Fatalf("resumed recv = %v, %v", ev, err)
	}
	again := next.unread()
	if string(again) != "{\"kind\":\"four\"}\n{\"ki" {
		t.Fatalf("a second hand-over's unread = %q", again)
	}
	go func() { _, _ = a.Write([]byte("nd\":\"five\"}\n")) }()
	if err := next.recv(&ev); err != nil || ev.Kind != "four" {
		t.Fatalf("recv = %v, %v", ev, err)
	}
	if err := next.recv(&ev); err != nil || ev.Kind != "five" {
		t.Fatalf("a line across the backlog and the socket = %v, %v", ev, err)
	}
}

func TestChannelRefusesAnOverlongLine(t *testing.T) {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	go func() {
		_, _ = a.Write(bytes.Repeat([]byte("x"), maxLine+5000))
		_ = a.Close()
	}()
	var ev event
	if err := newChannel(b).recv(&ev); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("recv = %v", err)
	}
}

func TestAWorkerThatIsNotAChildIsNotAdopted(t *testing.T) {
	_, err := adoptSession(handedSession{WorkerPID: os.Getppid(), Channel: 3, Protocol: workerProtocol, Cmd: []string{"niri"}}, func(io.Closer) {})
	if err == nil || !strings.Contains(err.Error(), "not this daemon") {
		t.Fatalf("adoptSession = %v", err)
	}
}

func TestResumeRefusesAnotherRuntimeDir(t *testing.T) {
	h := goodHandover()
	if err := h.sameRuntimeDir(Options{RuntimeDir: "/run/lys-login/"}); err != nil {
		t.Errorf("the same directory: %v", err)
	}
	if err := h.sameRuntimeDir(Options{RuntimeDir: "/run/other"}); err == nil || !strings.Contains(err.Error(), "/run/other") {
		t.Errorf("another directory: %v", err)
	}
}

func TestParentOfReadsPastACommandWithParens(t *testing.T) {
	got, err := parentOf([]byte("42 (a) b) (c) S 7 42 42 0 -1"))
	if err != nil || got != 7 {
		t.Fatalf("parentOf = %d, %v", got, err)
	}
	for _, bad := range []string{"", "42 (a", "42 (a) S", "42 (a) S x"} {
		if _, err := parentOf([]byte(bad)); err == nil {
			t.Errorf("parentOf(%q) took it", bad)
		}
	}
}

func TestForwardEnvDropsOnlyTheOldLibraryPath(t *testing.T) {
	env := []string{"A=1", "LD_LIBRARY_PATH=/nix/store/old/lib", "JOURNAL_STREAM=1:2", "LD_LIBRARY_PATH_X=y"}
	want := []string{"A=1", "JOURNAL_STREAM=1:2", "LD_LIBRARY_PATH_X=y"}
	if got := forwardEnv(env); !slices.Equal(got, want) {
		t.Errorf("forwardEnv = %q, want %q", got, want)
	}
	if env[1] != "LD_LIBRARY_PATH=/nix/store/old/lib" {
		t.Errorf("forwardEnv changed its argument: %q", env)
	}
}

func TestPreflightOutputStaysBounded(t *testing.T) {
	var out tailBuffer
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", 3*preflightOutput)+"the end")); err != nil {
		t.Fatal(err)
	}
	if len(out.b) != preflightOutput || !strings.HasSuffix(string(out.b), "the end") {
		t.Errorf("kept %d bytes ending %q", len(out.b), out.b[len(out.b)-7:])
	}
}

// fakeLiveSessionEnv has a fake daemon start with a session's lock held, as a
// daemon restarted over a worker that outlived the last one does; SIGUSR1
// lets it go.
const fakeLiveSessionEnv = "LYS_LOGIN_FAKE_LIVE_SESSION"

// Stopping the daemon ends the session and then the daemon, in good time:
// systemd would otherwise wait out its timeout and orphan the session.
func TestStoppingTheDaemonEndsTheSessionFirst(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	worker, _ := d.login()
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if err := syscall.Kill(d.pid, sig); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-d.done:
		d.done <- err
		if err != nil {
			t.Fatalf("the daemon: %v", err)
		}
	case <-time.After(stepDeadline):
		t.Fatalf("the daemon did not stop with a session running:\n%s", d.logged())
	}
	if _, err := os.Stat(fmt.Sprintf("/proc/%d", worker)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("worker %d outlived the daemon: %v", worker, err)
	}
	if !strings.Contains(d.logged(), "stopping: ending the session") {
		t.Errorf("the daemon did not say it ended the session:\n%s", d.logged())
	}
}

// A session keeps running after an unrelated request while the daemon runs:
// only a stop ends it.
func TestTheSessionRunsOnUntilTheDaemonStops(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	worker, _ := d.login()
	time.Sleep(100 * time.Millisecond)
	if err := syscall.Kill(worker, 0); err != nil {
		t.Fatalf("worker %d ended with nothing asking it to: %v", worker, err)
	}
	if !d.tracks() {
		t.Error("the daemon let go of a running session")
	}
}

// A daemon restarted over a session it did not start shows no greeter on top
// of it, and shows one once it ends.
func TestARestartLeavesALiveSessionAlone(t *testing.T) {
	d := startFakeDM(t, fakeLiveSessionEnv+"=1")
	d.await("the daemon noticing the session", func() bool { return strings.Contains(d.logged(), "the greeter waits for it to end") })
	if got := d.lines("greeters"); len(got) != 0 {
		t.Fatalf("a greeter started over the live session: %v", got)
	}
	if err := syscall.Kill(d.pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	d.awaitGreeter(1)
}

func stoppedDaemonStops(t *testing.T, dir string) []int {
	t.Helper()
	var stopped []int
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := console{
		activate: func(int) error { t.Error("a stopped daemon took the VT"); return nil },
		showText: func(int, bool) error { return nil },
		cards:    newHeldCards(),
		stop:     func(worker int) error { stopped = append(stopped, worker); return nil },
	}
	if err := runDaemon(ctx, Options{RuntimeDir: dir}, c, rootOnly, nil); err != nil {
		t.Fatal(err)
	}
	return stopped
}

func TestADaemonStoppedBeforeItLooksStillEndsTheLiveSession(t *testing.T) {
	dir := sockdir.Dir(t)
	held, err := holdSessionLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	if got := stoppedDaemonStops(t, dir); !slices.Equal(got, []int{os.Getpid()}) {
		t.Errorf("stopped workers %v, want the session's", got)
	}
}

func TestADaemonStoppedWithNoSessionStopsNothing(t *testing.T) {
	if got := stoppedDaemonStops(t, sockdir.Dir(t)); len(got) != 0 {
		t.Errorf("stopped workers %v with no session running", got)
	}
}

// The service sets SendSIGHUP, so a stop arrives as SIGTERM and SIGHUP: both
// must end the daemon through its context, not its default disposition.
func TestStopSignalsCoverWhatSystemdSends(t *testing.T) {
	for _, want := range []os.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		if !slices.Contains(StopSignals, want) {
			t.Errorf("%v is no stop signal", want)
		}
	}
}

// The console stays off the screen from the first greeter on: past the greeter
// a login ends, and past a session that ends. A stop gives the console back.
func TestTheConsoleStaysHiddenBetweenCompositorsAndComesBackOnAStop(t *testing.T) {
	d := startFakeDM(t)
	d.awaitGreeter(1)
	hidden := func(want int) {
		t.Helper()
		got := d.lines("console-modes")
		if len(got) != want || slices.Contains(got, "true") {
			t.Fatalf("console modes %v, want %d hidings and no text", got, want)
		}
	}
	hidden(1)
	worker, _ := d.login()
	hidden(2)
	d.endSession(worker)
	d.awaitGreeter(2)
	hidden(3)
	if err := syscall.Kill(d.pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	d.await("the console back", func() bool {
		got := d.lines("console-modes")
		return len(got) == 4 && got[3] == "true"
	})
}
