package dm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"
)

type sessionEvent struct {
	s *session
	e workerEvent
}

type greeterRun struct {
	vt       int
	held     io.Closer
	done     chan greetResult
	upgrades chan *pendingUpgrade
	cancel   context.CancelFunc
}

type greetResult struct {
	g   greeted
	err error
}

type seat struct {
	ctx      context.Context
	o        Options
	c        console
	owners   *sessionOwners
	warnings *log.Logger
	u        *upgrader

	mu       sync.Mutex
	sessions []*session

	events   chan sessionEvent
	switches chan struct{}
	greeter  *greeterRun
	retry    <-chan time.Time
	attempt  *upgradeAttempt
	stopping bool
	orphans  []int
}

func runDaemon(ctx context.Context, o Options, c console, may func(uint32) bool, r *resumption) error {
	if err := os.MkdirAll(o.RuntimeDir, 0o755); err != nil { //nolint:gosec // the greeter user reaches its socket through it
		return fmt.Errorf("dm: create %s: %w", o.RuntimeDir, err)
	}
	d := &seat{
		ctx: ctx, o: o, c: c, owners: newSessionOwners(),
		warnings: newWarningLogger(os.Stderr, os.Getenv(journalStreamEnv)),
		events:   make(chan sessionEvent), switches: make(chan struct{}),
	}
	var l *listeners
	if r != nil {
		l = r.adopted
	} else {
		var err error
		if l, err = listen(o.RuntimeDir); err != nil {
			return fmt.Errorf("dm: %w", err)
		}
	}
	defer func() { _ = l.close() }()
	if r != nil {
		for _, s := range r.sessions {
			d.adopt(s)
		}
	}
	gate := &sync.RWMutex{}
	d.u = &upgrader{o: o, requests: make(chan *pendingUpgrade), listeners: l, cards: c.cards, gate: gate, mayUpgrade: may, drain: loginDrain}
	go ApplySocket.serve(l.apply, applyChange(d.owners.authorizeApply(askPolkit), gate, o.Greeter.ConfigPath))
	go CursorSocket.serve(l.cursor, gated(gate, recordCursor(d.owners, o.CursorDir)))
	go SplashSocket.serve(l.splash, handOverSplash(o.isGreeter, quitSplash(o.Splash)))
	go SwitchSocket.serve(l.switchUser, gated(gate, askSwitch(d.owners, d.switches)))
	go d.u.serve()
	defer func() {
		if err := c.cards.Close(); err != nil {
			log.Printf("dm: %v", err)
		}
	}()
	defer func() {
		if err := c.showText(o.VT, true); err != nil {
			log.Printf("dm: %v", err)
		}
	}()
	if r != nil {
		r.finish(o)
	}
	d.run()
	d.stopOrphans()
	return nil
}

func (d *seat) adopt(s *session) {
	d.mu.Lock()
	d.sessions = append(d.sessions, s)
	d.mu.Unlock()
	if s.uid != nil {
		d.owners.add(*s.uid)
		log.Printf("dm: following the session of %s (uid %d) on VT %d, worker %d", s.user, *s.uid, s.vt, s.w.proc.Pid) //nolint:gosec // the daemon's own state
	} else {
		log.Printf("dm: following the session of %s starting on VT %d, worker %d", s.user, s.vt, s.w.proc.Pid) //nolint:gosec // the daemon's own state
	}
	d.read(s)
}

func (d *seat) read(s *session) {
	s.stop = make(chan struct{})
	events := s.w.events(s.stop)
	go func() {
		for e := range events {
			d.events <- sessionEvent{s, e}
		}
	}()
}

func (d *seat) running() []*session {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.sessions)
}

func (d *seat) sessionOf(user string) *session {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, s := range d.sessions {
		if s.user == user && s.id != "" {
			return s
		}
	}
	return nil
}

func (d *seat) onVT(vt int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.sessions, func(s *session) bool { return s.vt == vt })
}

func (d *seat) run() {
	done := d.ctx.Done()
	for {
		if !d.stopping && d.ctx.Err() != nil {
			done = nil
			d.stop()
		}
		if d.stopping && d.greeter == nil && len(d.running()) == 0 {
			return
		}
		if !d.stopping {
			if req := d.u.next(); req != nil {
				d.upgrade(req)
				continue
			}
			d.showSomething()
		}
		var greeterDone chan greetResult
		if d.greeter != nil {
			greeterDone = d.greeter.done
		}
		requests := d.u.requests
		if d.stopping {
			requests = nil
		}
		select {
		case <-done:
			done = nil
			d.stop()
		case ev := <-d.events:
			d.handle(ev)
		case <-d.switches:
			d.switchUser()
		case r := <-greeterDone:
			d.greeted(r)
		case req := <-requests:
			d.upgrade(req)
		case <-d.retry:
			d.retry = nil
		}
	}
}

func (d *seat) showSomething() {
	if d.greeter != nil || d.retry != nil || len(d.running()) > 0 {
		return
	}
	if d.waitForOrphans() {
		return
	}
	if d.autologin() {
		return
	}
	d.startGreeter(d.o.VT)
}

func (d *seat) waitForOrphans() bool {
	pids, err := liveSessions(d.o.RuntimeDir)
	if err != nil {
		log.Printf("dm: %v", err)
	}
	if len(pids) == 0 {
		d.orphans = nil
		return false
	}
	if !slices.Equal(pids, d.orphans) {
		log.Printf("dm: workers %v still run sessions this daemon did not start: the greeter waits for them to end", pids)
		d.orphans = pids
	}
	d.retry = time.After(retryPause)
	return true
}

func (d *seat) stopOrphans() {
	pids, err := liveSessions(d.o.RuntimeDir)
	if err != nil {
		log.Printf("dm: %v", err)
	}
	for _, pid := range pids {
		if err := d.c.stop(pid); err != nil {
			log.Printf("dm: stop the session of worker %d: %v", pid, err)
		}
	}
}

func (d *seat) startGreeter(vt int) {
	if err := d.c.activate(vt); err != nil {
		log.Printf("dm: %v", err)
	}
	d.c.hideText(vt)
	if err := d.c.cards.holdAll(); err != nil {
		log.Printf("dm: %v", err)
	}
	ctx, cancel := context.WithCancel(d.ctx)
	g := &greeterRun{vt: vt, done: make(chan greetResult, 1), upgrades: make(chan *pendingUpgrade), cancel: cancel}
	d.greeter = g
	go func() {
		res, err := d.o.greet(ctx, vt, g.upgrades, d.u.drain, func(user string) bool { return d.sessionOf(user) != nil })
		g.done <- greetResult{res, err}
	}()
}

func (d *seat) forward(a *upgradeAttempt) {
	select {
	case d.greeter.upgrades <- a.req:
		d.attempt = a
	case r := <-d.greeter.done:
		a.park()
		d.greeted(r)
	}
}

func (d *seat) greeted(r greetResult) {
	g := d.greeter
	d.greeter = nil
	g.cancel()
	a := d.attempt
	d.attempt = nil
	if a != nil && (r.err != nil || r.g.login != nil) {
		a.park()
		a = nil
	}
	switch {
	case r.err != nil:
		if !d.stopping {
			log.Printf("dm: greeter: %v", r.err)
			d.retry = time.After(retryPause)
		}
		d.leaveGreeterVT(g, nil)
	case r.g.login == nil:
		d.leaveGreeterVT(g, nil)
		if a != nil {
			d.exec(a)
		}
	case r.g.login.existing != "":
		s := d.sessionOf(r.g.login.existing)
		if s == nil {
			log.Printf("dm: the session of %s ended before the switch to it", r.g.login.existing)
		}
		d.leaveGreeterVT(g, s)
	default:
		d.start(r.g.login, g.vt, g.held)
	}
}

func letGo(held io.Closer, vt int) {
	if held == nil {
		return
	}
	if err := held.Close(); err != nil {
		log.Printf("dm: let go of VT %d: %v", vt, err)
	}
}

func (d *seat) leaveGreeterVT(g *greeterRun, to *session) {
	if sessions := d.running(); to == nil && !d.stopping && len(sessions) > 0 {
		to = sessions[len(sessions)-1]
	}
	if to != nil {
		d.activate(to)
	}
	letGo(g.held, g.vt)
	if g.vt != d.o.VT && !d.onVT(g.vt) {
		d.freeVT(g.vt)
	}
}

func (d *seat) freeVT(vt int) {
	if err := d.c.release(vt); err != nil {
		log.Printf("dm: %v", err)
	}
}

func (d *seat) activate(s *session) {
	d.mu.Lock()
	i := slices.Index(d.sessions, s)
	if i >= 0 {
		d.sessions = append(slices.Delete(d.sessions, i, i+1), s)
	}
	d.mu.Unlock()
	if s.id == "" {
		if err := d.c.activate(s.vt); err != nil {
			log.Printf("dm: %v", err)
		}
		return
	}
	if err := d.c.switchTo(s.id); err != nil {
		log.Printf("dm: switch to the session of %s: %v", s.user, err)
		return
	}
	if err := d.c.activate(s.vt); err != nil {
		log.Printf("dm: %v", err)
	}
	log.Printf("dm: switched to the session of %s on VT %d", s.user, s.vt)
}

func (d *seat) start(l *pendingLogin, vt int, held io.Closer) {
	d.c.hideText(vt)
	if err := l.w.ch.send(request{Op: opStart, Cmd: l.cmd, Env: l.env}); err != nil {
		l.w.wait()
		letGo(held, vt)
		log.Printf("dm: session: %v", err)
		return
	}
	s := &session{w: l.w, cmd: l.cmd, vt: vt, user: l.user, held: held}
	d.mu.Lock()
	d.sessions = append(d.sessions, s)
	d.mu.Unlock()
	d.read(s)
}

func (d *seat) handle(ev sessionEvent) {
	s := ev.s
	if !slices.Contains(d.running(), s) {
		return
	}
	if ev.e.err == nil {
		switch ev.e.ev.Kind {
		case evStarted:
			uid := ev.e.ev.UID
			s.uid, s.id = &uid, ev.e.ev.SessionID
			letGo(s.held, s.vt)
			s.held = nil
			d.owners.add(uid)
			log.Printf("dm: session %s of %s (uid %d) started on VT %d: %q", s.id, s.user, uid, s.vt, s.cmd)
			return
		case evKeyringLocked:
			d.warnings.Print(ev.e.ev.Text)
			return
		case evFailed:
			log.Printf("dm: session: the session of %s did not start: %s", s.user, ev.e.ev.Text)
		case evEnded:
		default:
			return
		}
	} else if !errors.Is(ev.e.err, errClosed) {
		log.Printf("dm: session of %s: %v", s.user, ev.e.err)
	}
	d.ended(s)
}

func (d *seat) ended(s *session) {
	close(s.stop)
	s.w.wait()
	letGo(s.held, s.vt)
	s.held = nil
	d.mu.Lock()
	d.sessions = slices.DeleteFunc(d.sessions, func(cur *session) bool { return cur == s })
	d.mu.Unlock()
	if s.uid != nil {
		d.owners.remove(*s.uid)
	}
	log.Printf("dm: the session of %s on VT %d ended", s.user, s.vt)
	if !d.stopping && d.greeter == nil {
		if sessions := d.running(); len(sessions) > 0 {
			d.activate(sessions[len(sessions)-1])
		} else if err := d.c.activate(d.o.VT); err != nil {
			log.Printf("dm: %v", err)
		}
	}
	if s.vt != d.o.VT && (d.greeter == nil || d.greeter.vt != s.vt) {
		d.freeVT(s.vt)
	}
}

func (d *seat) switchUser() {
	if d.stopping {
		return
	}
	if d.greeter != nil {
		if err := d.c.activate(d.greeter.vt); err != nil {
			log.Printf("dm: %v", err)
		}
		return
	}
	for _, s := range d.running() {
		if s.id == "" {
			continue
		}
		if err := d.c.lock(s.id); err != nil {
			log.Printf("dm: lock the session of %s before switching away: %v", s.user, err)
		}
	}
	vt, held, err := d.c.freeVT()
	if err != nil {
		log.Printf("dm: switch users: %v", err)
		return
	}
	log.Printf("dm: a greeter for another user on VT %d", vt)
	d.startGreeter(vt)
	d.greeter.held = held
}

func (d *seat) stop() {
	d.stopping = true
	if d.greeter != nil {
		d.greeter.cancel()
	}
	for _, s := range d.running() {
		log.Printf("dm: stopping: ending the session of %s on worker %d", s.user, s.w.proc.Pid)
		if err := s.w.proc.Signal(syscall.SIGTERM); err != nil {
			log.Printf("dm: stop the session's worker: %v", err)
		}
	}
}

func (d *seat) upgrade(req *pendingUpgrade) {
	a, err := d.u.prepare(req, d.running())
	if err != nil {
		req.fail(d.o.Self, err)
		return
	}
	if d.greeter != nil {
		d.forward(a)
		return
	}
	d.exec(a)
}

func (d *seat) exec(a *upgradeAttempt) {
	if !slices.Equal(a.sessions, d.running()) {
		a.park()
		return
	}
	if !d.quiesce(a.sessions) {
		a.park()
		d.resume(d.running())
		return
	}
	a.fail(a.exec())
	d.resume(a.sessions)
}

func (d *seat) quiesce(sessions []*session) bool {
	waiting := map[*session]bool{}
	for _, s := range sessions {
		if err := s.w.f.SetReadDeadline(time.Now()); err != nil {
			log.Printf("dm: stop reading the session of %s: %v", s.user, err)
			return false
		}
		waiting[s] = true
	}
	intact := true
	for len(waiting) > 0 {
		ev := <-d.events
		if errors.Is(ev.e.err, os.ErrDeadlineExceeded) {
			delete(waiting, ev.s)
			continue
		}
		before := len(d.running())
		d.handle(ev)
		if len(d.running()) < before {
			delete(waiting, ev.s)
			intact = false
		}
	}
	return intact
}

func (d *seat) resume(sessions []*session) {
	for _, s := range sessions {
		if err := s.w.f.SetReadDeadline(time.Time{}); err != nil {
			log.Printf("dm: read the session of %s again: %v", s.user, err)
		}
		d.read(s)
	}
}

const (
	autologinMarker = "autologin.done"
	autologinWait   = 30 * time.Second
)

func (d *seat) autologin() bool {
	a := d.o.Autologin
	if a == nil || !d.claimAutologin() {
		return false
	}
	w, err := d.o.startSessionWorker(a.User, d.o.VT, true)
	if err != nil {
		log.Printf("dm: autologin of %s: %v", a.User, err)
		return false
	}
	if err := awaitAuthenticated(w); err != nil {
		log.Printf("dm: autologin of %s: %v; the greeter shows instead", a.User, err)
		w.cancel()
		return false
	}
	if err := d.c.activate(d.o.VT); err != nil {
		log.Printf("dm: %v", err)
	}
	log.Printf("dm: autologin of %s into %q", a.User, a.Session.Cmd)
	d.start(&pendingLogin{w: w, user: a.User, cmd: a.Session.Cmd, env: a.Session.Env}, d.o.VT, nil)
	return true
}

func (d *seat) claimAutologin() bool {
	f, err := os.OpenFile(filepath.Join(d.o.RuntimeDir, autologinMarker), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return false
	}
	if err != nil {
		log.Printf("dm: autologin: %v; the greeter shows instead", err)
		return false
	}
	if err := f.Close(); err != nil {
		log.Printf("dm: autologin: %v", err)
	}
	return true
}

func awaitAuthenticated(w *worker) error {
	if err := w.f.SetReadDeadline(time.Now().Add(autologinWait)); err != nil {
		return fmt.Errorf("bound the authentication: %w", err)
	}
	var ev event
	err := w.ch.recv(&ev)
	if clearErr := w.f.SetReadDeadline(time.Time{}); clearErr != nil && err == nil {
		err = fmt.Errorf("unbind the authentication: %w", clearErr)
	}
	switch {
	case err != nil:
		return fmt.Errorf("the worker: %w", err)
	case ev.Kind == evFailed:
		return fmt.Errorf("PAM refused: %s", ev.Text)
	case ev.Kind != evAuthenticated:
		return fmt.Errorf("the worker answered %q, not authenticated", ev.Kind)
	}
	return nil
}
