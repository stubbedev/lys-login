package dm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stubbedev/lys-lib/peercred"
	"golang.org/x/sys/unix"
)

const (
	ResumeMode        = "daemon-resume"
	ResumeCheckFlag   = "check"
	ResumeStateFlag   = "state-fd"
	preflightOK       = "lys-login: the handover can be resumed"
	preflightStateFD  = firstHandoverFD
	preflightTimeout  = 10 * time.Second
	preflightOutput   = 4 << 10
	loginDrain        = 10 * time.Second
	upgradeQueueWait  = 30 * time.Second
	upgradeExchange   = 60 * time.Second
	libraryPathEnvKey = "LD_LIBRARY_PATH"
)

type UpgradeRequest struct {
	Exec string   `json:"exec"`
	Args []string `json:"args"`
}

type pendingUpgrade struct {
	UpgradeRequest
	conn *net.UnixConn
}

func (p *pendingUpgrade) fail(self string, cause error) {
	log.Printf("dm: the upgrade to %s failed, staying on %s: %v", p.Exec, self, cause) //nolint:gosec // root named the program
	if err := answerUpgrade(p.conn, "", cause); err != nil {
		log.Printf("dm: tell the upgrade's client: %v", err)
	}
}

func answerUpgrade(conn *net.UnixConn, path string, cause error) error {
	defer func() { _ = conn.Close() }()
	if err := conn.SetWriteDeadline(time.Now().Add(exchangeTimeout)); err != nil {
		return fmt.Errorf("bound the answer: %w", err)
	}
	r := reply{Path: path}
	if cause != nil {
		r = reply{Error: cause.Error()}
	}
	return send(conn, r, nil)
}

type upgrader struct {
	o          Options
	requests   chan *pendingUpgrade
	parked     *pendingUpgrade
	listeners  *listeners
	cards      *heldCards
	gate       *sync.RWMutex
	mayUpgrade func(uid uint32) bool
	drain      time.Duration
}

func rootOnly(uid uint32) bool { return uid == 0 }

func (u *upgrader) serve() {
	for {
		conn, err := u.listeners.upgrade.AcceptUnix()
		if err != nil {
			return
		}
		go func() {
			if err := u.take(conn); err != nil {
				log.Printf("dm: %s: %v", UpgradeSocket.name, err)
			}
		}()
	}
}

func (u *upgrader) take(conn *net.UnixConn) error {
	req, err := u.read(conn)
	if err != nil {
		return errors.Join(err, answerUpgrade(conn, "", err))
	}
	p := &pendingUpgrade{UpgradeRequest: req, conn: conn}
	select {
	case u.requests <- p:
		return nil
	case <-time.After(upgradeQueueWait):
		err := fmt.Errorf("the daemon took no upgrade in %s: another is under way", upgradeQueueWait)
		return errors.Join(err, answerUpgrade(conn, "", err))
	}
}

func (u *upgrader) read(conn *net.UnixConn) (UpgradeRequest, error) {
	if err := conn.SetDeadline(time.Now().Add(exchangeTimeout)); err != nil {
		return UpgradeRequest{}, fmt.Errorf("bound the request: %w", err)
	}
	uid, err := peercred.UID(conn)
	if err != nil {
		return UpgradeRequest{}, fmt.Errorf("who asks: %w", err)
	}
	if !u.mayUpgrade(uid) {
		return UpgradeRequest{}, fmt.Errorf("only root may upgrade the display manager, not uid %d", uid)
	}
	var req UpgradeRequest
	if err := json.NewDecoder(io.LimitReader(conn, UpgradeSocket.maxRequest)).Decode(&req); err != nil {
		return UpgradeRequest{}, fmt.Errorf("malformed upgrade request: %w", err)
	}
	if !filepath.IsAbs(req.Exec) {
		return UpgradeRequest{}, fmt.Errorf("the new program %q is not an absolute path", req.Exec)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return UpgradeRequest{}, fmt.Errorf("clear the request's deadline: %w", err)
	}
	return req, nil
}

func (u *upgrader) next() *pendingUpgrade {
	if p := u.parked; p != nil {
		u.parked = nil
		return p
	}
	select {
	case p := <-u.requests:
		return p
	default:
		return nil
	}
}

type upgradeAttempt struct {
	u       *upgrader
	req     *pendingUpgrade
	base    handover
	channel int
	dups    []int
}

func (u *upgrader) prepare(req *pendingUpgrade, s *session) (*upgradeAttempt, error) {
	a := &upgradeAttempt{u: u, req: req, channel: -1}
	if err := a.collect(s); err != nil {
		a.closeDups()
		return nil, err
	}
	data, err := a.state(s, nil).encode()
	if err != nil {
		a.closeDups()
		return nil, err
	}
	if err := preflight(req.UpgradeRequest, data); err != nil {
		a.closeDups()
		return nil, err
	}
	return a, nil
}

func (a *upgradeAttempt) collect(s *session) error {
	var err error
	take := func(c syscall.Conn, what string) int {
		if err != nil {
			return -1
		}
		var fd int
		fd, err = a.dupConn(c)
		if err != nil {
			err = fmt.Errorf("keep %s across the exec: %w", what, err)
		}
		return fd
	}
	a.base = handover{
		Version:    handoverVersion,
		From:       a.u.o.Self,
		RuntimeDir: a.u.o.RuntimeDir,
		Client:     take(a.req.conn, "the upgrade's client"),
		Listeners: handedListeners{
			Apply:   take(a.u.listeners.apply, ApplySocket.name),
			Cursor:  take(a.u.listeners.cursor, CursorSocket.name),
			Splash:  take(a.u.listeners.splash, SplashSocket.name),
			Upgrade: take(a.u.listeners.upgrade, UpgradeSocket.name),
		},
		Rollback: rollback{Args: a.u.o.Args, Env: os.Environ()},
	}
	if s != nil {
		a.channel = take(s.w.f, "the session's channel")
	}
	if err != nil {
		return err
	}
	if a.base.Cards, err = a.dupCards(); err != nil {
		return err
	}
	exe, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("keep the running program to roll back to: %w", err)
	}
	a.dups = append(a.dups, exe)
	if exe < firstHandoverFD {
		return fmt.Errorf("the running program is on descriptor %d, below %d", exe, firstHandoverFD)
	}
	a.base.Rollback.Exe = exe
	return nil
}

func (a *upgradeAttempt) dupConn(c syscall.Conn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return -1, err
	}
	dup := -1
	var dupErr error
	if err := raw.Control(func(fd uintptr) {
		dup, dupErr = a.dupFD(int(fd))
	}); err != nil {
		return -1, err
	}
	return dup, dupErr
}

func (a *upgradeAttempt) dupFD(fd int) (int, error) {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, firstHandoverFD)
	if err != nil {
		return -1, fmt.Errorf("duplicate descriptor %d: %w", fd, err)
	}
	a.dups = append(a.dups, dup)
	return dup, nil
}

func (a *upgradeAttempt) dupCards() ([]handedCard, error) {
	held := a.u.cards.held()
	cards := make([]handedCard, 0, len(held))
	for _, card := range held {
		dup, err := a.dupFD(card.FD)
		if err != nil {
			return nil, fmt.Errorf("keep %s across the exec: %w", card.Path, err)
		}
		cards = append(cards, handedCard{Path: card.Path, FD: dup})
	}
	return cards, nil
}

func (a *upgradeAttempt) state(s *session, unread []byte) handover {
	h := a.base
	if s != nil {
		h.Session = &handedSession{
			WorkerPID: s.w.proc.Pid,
			Channel:   a.channel,
			Protocol:  workerProtocol,
			Unread:    unread,
			UID:       s.uid,
			Cmd:       s.cmd,
		}
	}
	return h
}

func (a *upgradeAttempt) closeDups() {
	for _, fd := range a.dups {
		_ = unix.Close(fd)
	}
	a.dups = nil
}

func (a *upgradeAttempt) park() {
	a.closeDups()
	a.u.parked = a.req
}

func (a *upgradeAttempt) fail(cause error) {
	a.closeDups()
	a.req.fail(a.u.o.Self, cause)
}

func (a *upgradeAttempt) exec(s *session) error {
	a.u.gate.Lock()
	defer a.u.gate.Unlock()
	var unread []byte
	if s != nil {
		unread = s.w.ch.unread()
	}
	h := a.state(s, unread)
	data, err := h.encode()
	if err != nil {
		return err
	}
	state, err := stateMemfd(data)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(state) }()
	log.Printf("dm: upgrading in place from %s to %s", a.u.o.Self, a.req.Exec) //nolint:gosec // root named the program
	return execKeeping(a.req.Exec, a.req.Exec, a.req.Args, forwardEnv(os.Environ()), state, h)
}

func execKeeping(path, argv0 string, args, env []string, state int, h handover) error {
	argv := append([]string{argv0, ResumeMode, "--" + ResumeStateFlag, strconv.Itoa(state), "--"}, args...)
	fds := make([]int, 0, len(h.descriptors())+1)
	fds = append(fds, state)
	for _, f := range h.descriptors() {
		fds = append(fds, f.fd)
	}
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return errors.Join(fmt.Errorf("keep descriptor %d across the exec: %w", fd, err), closeOnExec(fds))
		}
	}
	err := syscall.Exec(path, argv, env) //nolint:gosec // root named this program, and preflight ran it
	return errors.Join(fmt.Errorf("exec %s: %w", path, err), closeOnExec(fds))
}

func closeOnExec(fds []int) error {
	var errs []error
	for _, fd := range fds {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
			errs = append(errs, fmt.Errorf("close descriptor %d on exec: %w", fd, err))
		}
	}
	return errors.Join(errs...)
}

func stateMemfd(data []byte) (int, error) {
	fd, err := unix.MemfdCreate("lys-login-handover", unix.MFD_CLOEXEC)
	if err != nil {
		return -1, fmt.Errorf("create the state to hand over: %w", err)
	}
	for rest := data; len(rest) > 0; {
		n, err := unix.Write(fd, rest)
		if err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("write the state to hand over: %w", err)
		}
		rest = rest[n:]
	}
	return fd, nil
}

func forwardEnv(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		return strings.HasPrefix(kv, libraryPathEnvKey+"=")
	})
}

type tailBuffer struct {
	b []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if extra := len(t.b) - preflightOutput; extra > 0 {
		t.b = t.b[extra:]
	}
	return len(p), nil
}

func preflight(req UpgradeRequest, data []byte) error {
	state, err := stateMemfd(data)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(state), "the handover")
	defer func() { _ = f.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), preflightTimeout)
	defer cancel()
	argv := append([]string{ResumeMode, "--" + ResumeCheckFlag, "--" + ResumeStateFlag, strconv.Itoa(preflightStateFD), "--"}, req.Args...)
	cmd := exec.CommandContext(ctx, req.Exec, argv...) //nolint:gosec // root named this program
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = forwardEnv(os.Environ())
	var out tailBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	said := strings.TrimSpace(string(out.b))
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%s did not finish its check in %s: %s", req.Exec, preflightTimeout, said)
	case err != nil:
		return fmt.Errorf("%s refused the handover: %w: %s", req.Exec, err, said)
	case !slices.Contains(strings.Split(said, "\n"), preflightOK):
		return fmt.Errorf("%s did not confirm the handover: %q", req.Exec, said)
	}
	return nil
}

func readHandover(fd int) (handover, error) {
	f := os.NewFile(uintptr(fd), "the handover")
	if f == nil {
		return handover{}, fmt.Errorf("no handed-over state on descriptor %d", fd)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.NewSectionReader(f, 0, maxHandover+1))
	if err != nil {
		return handover{}, fmt.Errorf("read the handed-over state on descriptor %d: %w", fd, err)
	}
	if len(data) > maxHandover {
		return handover{}, fmt.Errorf("the handed-over state is over the %d-byte limit", maxHandover)
	}
	return parseHandover(data)
}

func CheckResume(stateFD int, o Options, out io.Writer) error {
	h, err := readHandover(stateFD)
	if err != nil {
		return err
	}
	if err := h.sameRuntimeDir(o); err != nil {
		return err
	}
	if _, err := lookupAccount(o.GreeterUser); err != nil {
		return fmt.Errorf("the greeter's account: %w", err)
	}
	if _, err := exec.LookPath(o.Compositor[0]); err != nil {
		return fmt.Errorf("the greeter's compositor: %w", err)
	}
	_, err = fmt.Fprintln(out, preflightOK)
	return err
}

type resumption struct {
	from    string
	client  *net.UnixConn
	failure string
	session *session
	adopted *listeners
}

func Resume(ctx context.Context, o Options, stateFD int) error {
	return resume(ctx, o, systemConsole(), rootOnly, stateFD)
}

func resume(ctx context.Context, o Options, c console, may func(uint32) bool, stateFD int) error {
	h, err := readHandover(stateFD)
	if err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	r, err := adoptFor(h, o, c.cards)
	if err != nil {
		if h.Failure != "" {
			return fmt.Errorf("resume the old daemon after %q: %w", h.Failure, err)
		}
		return rollBack(h, o.Self, err)
	}
	return runDaemon(ctx, o, c, may, r)
}

func rollBack(h handover, self string, cause error) error {
	log.Printf("dm: %s cannot resume, going back to %s: %v", self, h.From, cause)
	h.Failure = fmt.Sprintf("%s could not resume: %v", self, cause)
	data, err := h.encode()
	if err != nil {
		return errors.Join(cause, err)
	}
	state, err := stateMemfd(data)
	if err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, execKeeping(fmt.Sprintf("/proc/self/fd/%d", h.Rollback.Exe), h.From, h.Rollback.Args, h.Rollback.Env, state, h))
}

func adoptFor(h handover, o Options, cards *heldCards) (*resumption, error) {
	if err := h.sameRuntimeDir(o); err != nil {
		return nil, err
	}
	return adopt(h, cards)
}

func adopt(h handover, cards *heldCards) (r *resumption, err error) {
	defer func() {
		if p := recover(); p != nil {
			r, err = nil, fmt.Errorf("resuming panicked: %v", p)
		}
	}()
	descriptors := h.descriptors()
	for _, f := range descriptors {
		if _, err := unix.FcntlInt(uintptr(f.fd), unix.F_GETFD, 0); err != nil {
			return nil, fmt.Errorf("%s on descriptor %d: %w", f.what, f.fd, err)
		}
	}
	var built []io.Closer
	defer func() {
		if err != nil {
			for _, c := range built {
				_ = c.Close()
			}
		}
	}()
	track := func(c io.Closer) { built = append(built, c) }
	r = &resumption{from: h.From, failure: h.Failure}
	if r.adopted, err = adoptListeners(h.Listeners, track); err != nil {
		return nil, err
	}
	if r.client, err = adoptConn(h.Client, track); err != nil {
		return nil, fmt.Errorf("the upgrade's client: %w", err)
	}
	if h.Session != nil {
		if r.session, err = adoptSession(*h.Session, track); err != nil {
			return nil, fmt.Errorf("the session of worker %d: %w", h.Session.WorkerPID, err)
		}
	}
	if err := cards.adopt(h.Cards, track); err != nil {
		return nil, err
	}
	for _, f := range descriptors {
		_ = unix.Close(f.fd)
	}
	return r, nil
}

func adoptDup(fd int) (*os.File, error) {
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, firstHandoverFD)
	if err != nil {
		return nil, fmt.Errorf("duplicate descriptor %d: %w", fd, err)
	}
	return os.NewFile(uintptr(dup), "handed over"), nil
}

func adoptListeners(h handedListeners, track func(io.Closer)) (*listeners, error) {
	listener := func(fd int, name string) (*net.UnixListener, error) {
		f, err := adoptDup(fd)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		defer func() { _ = f.Close() }()
		ln, err := net.FileListener(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		track(ln)
		unixLn, ok := ln.(*net.UnixListener)
		if !ok {
			return nil, fmt.Errorf("%s: a %T, not a unix socket", name, ln)
		}
		unixLn.SetUnlinkOnClose(true)
		return unixLn, nil
	}
	var l listeners
	var err error
	if l.apply, err = listener(h.Apply, ApplySocket.name); err != nil {
		return nil, err
	}
	if l.cursor, err = listener(h.Cursor, CursorSocket.name); err != nil {
		return nil, err
	}
	if l.splash, err = listener(h.Splash, SplashSocket.name); err != nil {
		return nil, err
	}
	if l.upgrade, err = listener(h.Upgrade, UpgradeSocket.name); err != nil {
		return nil, err
	}
	return &l, nil
}

func adoptConn(fd int, track func(io.Closer)) (*net.UnixConn, error) {
	f, err := adoptDup(fd)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	conn, err := net.FileConn(f)
	if err != nil {
		return nil, err
	}
	track(conn)
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("a %T, not a unix socket", conn)
	}
	return unixConn, nil
}

func adoptSession(h handedSession, track func(io.Closer)) (*session, error) {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", h.WorkerPID))
	if err != nil {
		return nil, fmt.Errorf("find the worker: %w", err)
	}
	ppid, err := parentOf(stat)
	if err != nil {
		return nil, err
	}
	if ppid != os.Getpid() {
		return nil, fmt.Errorf("the worker's parent is %d, not this daemon, %d", ppid, os.Getpid())
	}
	proc, err := os.FindProcess(h.WorkerPID)
	if err != nil {
		return nil, fmt.Errorf("find the worker: %w", err)
	}
	f, err := adoptDup(h.Channel)
	if err != nil {
		return nil, fmt.Errorf("the channel: %w", err)
	}
	track(f)
	return &session{w: &worker{proc: proc, ch: resumeChannel(f, h.Unread), f: f}, cmd: h.Cmd, uid: h.UID}, nil
}

func parentOf(stat []byte) (int, error) {
	_, rest, found := bytes.CutLast(stat, []byte{')'})
	if !found {
		return 0, fmt.Errorf("the stat line %q has no command", stat)
	}
	fields := strings.Fields(string(rest))
	if len(fields) < 2 {
		return 0, fmt.Errorf("the stat line %q has no parent", stat)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, fmt.Errorf("the stat line %q: %w", stat, err)
	}
	return ppid, nil
}

func (r *resumption) finish(o Options, owner *sessionOwner, warnings *log.Logger, u *upgrader) {
	if r.failure != "" {
		log.Printf("dm: rolled back to %s: %s", o.Self, r.failure) //nolint:gosec // this daemon's own state
		if err := answerUpgrade(r.client, "", errors.New(r.failure)); err != nil {
			log.Printf("dm: tell the upgrade's client: %v", err)
		}
	} else {
		log.Printf("dm: upgraded in place from %s to %s", r.from, o.Self) //nolint:gosec // this daemon's own state
		if err := answerUpgrade(r.client, o.Self, nil); err != nil {
			log.Printf("dm: tell the upgrade's client: %v", err)
		}
	}
	if r.session == nil {
		return
	}
	if r.session.uid != nil {
		log.Printf("dm: following the session of uid %d on worker %d", *r.session.uid, r.session.w.proc.Pid) //nolint:gosec // numbers
	} else {
		log.Printf("dm: following the session starting on worker %d", r.session.w.proc.Pid) //nolint:gosec // a number
	}
	err := r.session.follow(owner.set, warnings, u)
	owner.clear()
	if err != nil {
		log.Printf("dm: session: %v", err)
	}
}
