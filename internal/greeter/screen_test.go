package greeter

import (
	"bytes"
	"errors"
	"image"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-shell/config"
	"github.com/stubbedev/lys-shell/imagedecode"
	"github.com/stubbedev/lys-shell/shell/treetest"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/lys-ui/widget/css"
	"github.com/stubbedev/memory-go"

	"github.com/stubbedev/lys-login/internal/auth"
	loginscreen "github.com/stubbedev/lys-login/internal/screen"
)

// fakeHandle is one greetd conversation the screen spawned.
type fakeHandle struct {
	username  string
	answers   []string
	cancelled bool
	emit      func(auth.Event)
}

func (h *fakeHandle) Answer(s string) { h.answers = append(h.answers, s) }
func (h *fakeHandle) Cancel()         { h.cancelled = true }

// rig is a screen over fakes: no greetd, no compositor.
type scheduled struct {
	d  time.Duration
	fn func()
}

type rig struct {
	scheduled  []scheduled
	s          *screen
	convs      []*fakeHandle
	starts     []auth.Start
	presented  []func()
	timeout    chan time.Time
	connectErr error
	focused    widget.Widget
	keyboard   bool
	quit       int
	powerMu    sync.Mutex
	power      []string
	onPower    func(verb string)
	// background is what the background image decodes to; nil fails
	// the decode.
	background image.Image
	// outputs are the device sizes the greeter shows on.
	outputs []image.Point
	// forgotten are the rasters the renderer was told to drop.
	forgotten []render.ImageID
}

func newRig(t *testing.T, edit func(*Init)) *rig {
	t.Helper()
	return buildRig(t, edit, &rig{outputs: []image.Point{{1920, 1080}}, timeout: make(chan time.Time)})
}

// buildRig builds r's screen over its fakes, with Init edited by edit.
func buildRig(t *testing.T, edit func(*Init), r *rig) *rig {
	t.Helper()
	face := treetest.Font(t)
	state := filepath.Join(t.TempDir(), "last-session")
	init := Init{
		Config: config.Defaults(),
		Sessions: []Session{
			{ID: "niri", Name: "Niri", Exec: []string{"niri-session"}},
			{ID: "sway", Name: "Sway", Exec: []string{"sway"}},
			{ID: "plasma-x11", Kind: X11, Name: "Plasma (X11)", Exec: []string{"startx", "/usr/bin/env", "startplasma-x11"}},
		},
		Users:      []User{{Name: "ada", DisplayName: "Ada", Home: "/home/ada"}, {Name: "bob", DisplayName: "Bob", Home: "/home/bob"}},
		StatePath:  state,
		SessionEnv: []string{"LANG=C"},
	}
	init.Config.Login.Greeter.ShowUserList = true
	init.Config.Login.Greeter.ShowPowerButtons = true
	if edit != nil {
		edit(&init)
	}
	r.s = newScreen(init, face, Stylesheet(init.Config), deps{
		connect: func(start auth.Start) (auth.Conversation, error) {
			if r.connectErr != nil {
				return nil, r.connectErr
			}
			r.starts = append(r.starts, start)
			return nil, nil
		},
		spawn: func(_ auth.Conversation, username string, onEvent func(auth.Event)) authHandle {
			h := &fakeHandle{username: username, emit: onEvent}
			r.convs = append(r.convs, h)
			return h
		},
		invoke:       func(fn func()) { fn() },
		focus:        func(w widget.Widget) { r.focused = w },
		focused:      func() widget.Widget { return r.focused },
		keyboardHeld: func() bool { return r.keyboard },
		quit:         func() { r.quit++ },
		whenPresented: func(fn func()) error {
			r.presented = append(r.presented, fn)
			return nil
		},
		after: func(time.Duration) <-chan time.Time { return r.timeout },
		schedule: func(d time.Duration, fn func()) func() {
			r.scheduled = append(r.scheduled, scheduled{d, fn})
			return func() {}
		},
		now: func() time.Time { return time.Date(2026, 3, 5, 9, 7, 0, 0, time.UTC) },
		power: func(verb string) {
			if r.onPower != nil {
				r.onPower(verb)
			}
			r.powerMu.Lock()
			defer r.powerMu.Unlock()
			r.power = append(r.power, verb)
		},
		loadImage: func(*memory.Shared, string) (imagedecode.Decoded, error) {
			if r.background == nil {
				return imagedecode.Decoded{}, errors.New("none")
			}
			return imagedecode.Decoded{Image: r.background}, nil
		},
		outputs:    func() []image.Point { return r.outputs },
		forget:     func(id render.ImageID) { r.forgotten = append(r.forgotten, id) },
		loadAvatar: func(string) (image.Image, error) { return nil, errors.New("none") },
	})
	return r
}

// powerVerbs waits for n power actions to have run and returns them sorted: each
// runs on its own goroutine.
func (r *rig) powerVerbs(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.powerMu.Lock()
		got := slices.Sorted(slices.Values(r.power))
		r.powerMu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d power actions ran in 5s, want %d", len(got), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *rig) conv(t *testing.T) *fakeHandle {
	t.Helper()
	if len(r.convs) == 0 {
		t.Fatal("no conversation started")
	}
	return r.convs[len(r.convs)-1]
}

func prompt(kind auth.PromptKind, text string) auth.Event {
	return auth.Event{Kind: auth.EventPrompt, Prompt: auth.Prompt{Kind: kind, Text: text}}
}

func TestGreeterStartsRevealedAndFocused(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	if !r.s.prompt.Reveal.Revealed() {
		t.Error("the card does not reveal")
	}
	if r.focused != r.s.prompt.Username {
		t.Error("with nobody remembered the username takes focus")
	}
	if r.s.prompt.Username.Placeholder() != "Username" {
		t.Errorf("placeholder = %q, want the localized one", r.s.prompt.Username.Placeholder())
	}

	r = newRig(t, func(i *Init) { i.LastUser = "ada"; i.LastSession = "sway" })
	r.s.start()
	if r.s.prompt.Username.Text() != "ada" || r.focused != r.s.prompt.Entry {
		t.Error("a remembered user pre-fills and the password takes focus")
	}
	if r.s.selectedSession().ID != "sway" {
		t.Errorf("selected session = %q, want the remembered sway", r.s.selectedSession().ID)
	}
}

// readyLines runs fn and returns the readiness lines it logged.
func readyLines(t *testing.T, fn func()) []string {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fn()
	var lines []string
	for line := range strings.Lines(buf.String()) {
		if strings.Contains(line, "greeter: ready:") {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestGreeterAnnouncesReadyOnceFormShownAndKeyboardHeld(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Init)
		field string
	}{
		{"nobody remembered", nil, "the username field"},
		{"a remembered user", func(i *Init) { i.LastUser = "ada" }, "the password field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.edit)
			r.s.start()
			r.keyboard = true
			lines := readyLines(t, func() {
				r.s.markPresented()
				r.s.announceReady()
				r.s.markPresented()
			})
			if len(lines) != 1 || !strings.Contains(lines[0], tc.field) {
				t.Errorf("logged %q, want one line naming %s", lines, tc.field)
			}
		})
	}
}

func TestGreeterAnnouncesReadyWhicheverComesLast(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	lines := readyLines(t, func() {
		r.s.markPresented()
		r.keyboard = true
		r.s.announceReady()
	})
	if len(lines) != 1 {
		t.Errorf("a keyboard arriving after the first frame logged %q, want one line", lines)
	}
}

func TestGreeterIsNotReadyWithoutTheFrameTheKeyboardOrTheField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*rig)
	}{
		{"no frame on screen", func(r *rig) { r.s.start(); r.keyboard = true; r.s.announceReady() }},
		{"no keyboard from the compositor", func(r *rig) { r.s.start(); r.s.markPresented() }},
		{"no field focused", func(r *rig) { r.keyboard = true; r.s.markPresented() }},
		{"focus off the form", func(r *rig) {
			r.s.start()
			r.keyboard = true
			r.focused = r.s.drop
			r.s.markPresented()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			if lines := readyLines(t, func() { tc.setup(r) }); len(lines) != 0 {
				t.Errorf("logged %q before the form takes keys", lines)
			}
		})
	}
}

func TestGreeterLoginSucceeds(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText(" ada ")
	r.s.submit("hunter2")
	h := r.conv(t)
	if h.username != "ada" || r.s.prompt.Username.Enabled() || r.s.prompt.Entry.Enabled() {
		t.Fatalf("a submit starts a conversation for the trimmed user with the form locked: %+v", h)
	}
	if got := r.starts[0].Session().Env; !slices.Equal(got, []string{"XDG_SESSION_TYPE=wayland", "LANG=C"}) {
		t.Errorf("session env = %v", got)
	}
	// The command is read when greetd starts the session: a change of
	// mind mid-login is what runs.
	r.s.drop.OnSelect(1)
	if got := r.starts[0].Session().Cmd; !slices.Equal(got, []string{"sway"}) {
		t.Errorf("session command = %v, want the picked sway", got)
	}
	h.emit(prompt(auth.PromptSecret, "Password:"))
	if !slices.Equal(h.answers, []string{"hunter2"}) {
		t.Fatalf("answers = %v, want the stashed password", h.answers)
	}
	h.emit(auth.Event{Kind: auth.EventSuccess})
	if r.quit != 1 {
		t.Error("success did not hand over to greetd")
	}
}

func TestGreeterRemembersTheLoginBeforeTheSessionStarts(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText(" ada ")
	r.s.drop.OnSelect(1)
	r.s.submit("hunter2")
	if got := LoadLast(LastUserPath(r.s.init.StatePath)); got != "" {
		t.Fatalf("user %q remembered before the login succeeded", got)
	}
	close(r.timeout)
	r.starts[0].Before()
	if got := LoadLast(r.s.init.StatePath); got != "sway" {
		t.Errorf("remembered session = %q; the display manager may end the greeter as soon as the session starts", got)
	}
	if got := LoadLast(LastUserPath(r.s.init.StatePath)); got != "ada" {
		t.Errorf("remembered user = %q; the display manager may end the greeter as soon as the session starts", got)
	}
}

func TestGreeterRemembersNothingOfAFailedLogin(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("wrong")
	r.conv(t).emit(auth.Event{Kind: auth.EventFailure, Reason: "no"})
	if got := LoadLast(LastUserPath(r.s.init.StatePath)); got != "" {
		t.Errorf("a failed login remembered user %q", got)
	}
	if got := LoadLast(r.s.init.StatePath); got != "" {
		t.Errorf("a failed login remembered session %q", got)
	}
}

func TestGreeterSecondPromptAndInfo(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("pw")
	h := r.conv(t)
	h.emit(prompt(auth.PromptSecret, "Password:"))
	h.emit(prompt(auth.PromptInfo, "Checking"))
	if r.s.prompt.Error.Text() != "Checking" {
		t.Errorf("info = %q", r.s.prompt.Error.Text())
	}
	h.emit(prompt(auth.PromptVisible, "OTP:"))
	if !r.s.awaiting || !r.s.prompt.Entry.Enabled() || r.focused != r.s.prompt.Entry || r.s.prompt.Error.Text() != "OTP:" {
		t.Fatal("a second prompt re-enables the entry and waits")
	}
	r.s.submit("123456")
	if !slices.Equal(h.answers, []string{"pw", "123456"}) || r.s.prompt.Entry.Enabled() {
		t.Errorf("answers = %v; the entry locks again while greetd works", h.answers)
	}
	// A stray submit while greetd works is ignored.
	r.s.submit("again")
	if len(r.convs) != 1 || len(h.answers) != 2 {
		t.Error("a submit mid-conversation started or answered something")
	}
}

func TestGreeterFailureRearms(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("wrong")
	first := r.conv(t)
	first.emit(auth.Event{Kind: auth.EventFailure, Reason: "pam_authenticate: AUTH_ERR", Refusal: auth.RefusalCredentials})
	if r.s.prompt.Error.Text() != loginscreen.RefusalText(auth.RefusalCredentials) || !r.s.prompt.Entry.Enabled() || r.s.prompt.Username.Text() != "ada" {
		t.Fatal("a failure shows the reason, keeps the user and re-arms the form")
	}
	if r.quit != 0 {
		t.Error("a failure quit")
	}
	r.s.submit("right")
	if len(r.convs) != 2 {
		t.Fatal("the next submit did not start a fresh conversation")
	}
	// The dead conversation's late events change nothing.
	first.emit(auth.Event{Kind: auth.EventSuccess})
	if r.quit != 0 {
		t.Error("a stale conversation's success was taken")
	}
}

func TestGreeterRefusesAnEmptyUserAndANoGreetd(t *testing.T) {
	r := newRig(t, nil)
	r.s.submit("pw")
	if len(r.convs) != 0 || r.s.prompt.Error.Text() != "Enter a username" || r.focused != r.s.prompt.Username {
		t.Error("an empty username starts nothing and asks for one")
	}
	r.connectErr = errors.New("no GREETD_SOCK")
	r.s.prompt.Username.SetText("ada")
	r.s.submit("pw")
	if len(r.convs) != 0 || r.s.prompt.Error.Text() != "greetd unavailable: \u2068no GREETD_SOCK\u2069" || !r.s.prompt.Entry.Enabled() {
		t.Errorf("no greetd: message %q, entry enabled %v", r.s.prompt.Error.Text(), r.s.prompt.Entry.Enabled())
	}
}

func TestGreeterUserRowAndPower(t *testing.T) {
	r := newRig(t, nil)
	if len(r.s.users) != 2 {
		t.Fatalf("user buttons = %d", len(r.s.users))
	}
	r.s.users[1].button.OnClick()
	if r.s.prompt.Username.Text() != "bob" || r.focused != r.s.prompt.Entry || !slices.Contains(r.s.users[1].button.Classes(), "selected") || slices.Contains(r.s.users[0].button.Classes(), "selected") {
		t.Error("a user click fills the name, marks it and focuses the password")
	}
	many := make([]User, maxListedUsers+1)
	for i := range many {
		many[i] = User{Name: "u", DisplayName: "U"}
	}
	if r := newRig(t, func(i *Init) { i.Users = many }); len(r.s.users) != 0 {
		t.Error("more users than the cap still listed")
	}
	if r := newRig(t, func(i *Init) { i.Config.Login.Greeter.ShowUserList = false }); len(r.s.users) != 0 {
		t.Error("show-user-list off still listed users")
	}
	// The power buttons call systemctl's verbs.
	var buttons []*widget.Button
	var walk func(widget.Widget)
	walk = func(w widget.Widget) {
		if b, ok := w.(*widget.Button); ok && b.TooltipText() != "" {
			buttons = append(buttons, b)
		}
		if c, ok := w.(interface{ Children() []widget.Widget }); ok {
			for _, k := range c.Children() {
				walk(k)
			}
		}
	}
	walk(r.s.root)
	for _, b := range buttons {
		b.OnClick()
	}
	if got := r.powerVerbs(t, 2); !slices.Equal(got, []string{"poweroff", "reboot"}) {
		t.Errorf("power verbs = %v", got)
	}
	for _, b := range buttons {
		if !slices.Contains(b.Classes(), css.Ghost) || !slices.Contains(b.Classes(), css.Icon) || slices.Contains(b.Classes(), "flat") {
			t.Errorf("power button classes = %v, want lys-ui's ghost icon button", b.Classes())
		}
	}
}

// TestGreeterUserButtonsAreLysUi pins the user row on lys-ui's parts:
// each user is a ghost button, never the old flat one, and the avatar
// is lys-ui's Avatar with the user's initials.
func TestGreeterUserButtonsAreLysUi(t *testing.T) {
	r := newRig(t, nil)
	for i, ub := range r.s.users {
		if !slices.Contains(ub.button.Classes(), css.Ghost) || slices.Contains(ub.button.Classes(), "flat") {
			t.Errorf("user %d button classes = %v", i, ub.button.Classes())
		}
		want := ub.user.DisplayName[:1]
		if got := r.s.avatar(ub.user, treetest.Font(t)).Initials(); got != want {
			t.Errorf("user %d avatar initials = %q, want %q", i, got, want)
		}
	}
}

func TestGreeterSessionPickerAndCaps(t *testing.T) {
	if r := newRig(t, func(i *Init) { i.Sessions = i.Sessions[:1] }); r.s.drop.Visible() {
		t.Error("a single session still shows the picker")
	}
	r := newRig(t, nil)
	if !r.s.drop.Visible() {
		t.Error("two sessions hide the picker")
	}
	r.s.setCapsLock(true)
	if !r.s.prompt.Caps.Visible() || r.s.prompt.Caps.Text() != "Caps Lock is on" || !slices.Contains(r.s.prompt.Caps.Classes(), css.Warning) {
		t.Error("caps lock on shows the warning")
	}
	r.s.setCapsLock(false)
	if r.s.prompt.Caps.Visible() {
		t.Error("caps lock off keeps the warning")
	}
}

func TestCustomSessionName(t *testing.T) {
	if CustomSessionName() != "Custom" {
		t.Errorf("custom = %q", CustomSessionName())
	}
}

func TestGreeterLeavesOnlyItsBackgroundBeforeTheSessionStarts(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	r.s.prompt.Reveal.Finish()
	left := 0
	r.s.leave(func() { left++ })
	if r.s.prompt.Reveal.Revealed() || r.s.power.Visible() {
		t.Fatal("the card and the power buttons stay up as the session starts")
	}
	if len(r.presented) != 0 {
		t.Fatal("asked for the frame before the card finished fading out")
	}
	r.s.prompt.Reveal.Finish()
	if len(r.presented) != 1 || left != 0 {
		t.Fatalf("%d presentation waits, left %d; want one wait for the frame without the card", len(r.presented), left)
	}
	r.presented[0]()
	if left != 1 {
		t.Errorf("left %d times once that frame was presented, want 1", left)
	}
}

func TestGreeterHandOverWaitsForThePresentedFrameOrTheTimeout(t *testing.T) {
	noAnimations := func(i *Init) { i.Config.Animations.Enabled = false }
	r := newRig(t, noAnimations)
	r.s.start()
	r.s.d.whenPresented = func(fn func()) error { fn(); return nil }
	r.s.handOver()

	r = newRig(t, noAnimations)
	r.s.start()
	close(r.timeout)
	r.s.handOver()
	if len(r.presented) != 1 {
		t.Errorf("%d presentation waits, want the one the timeout cut short", len(r.presented))
	}
}

func TestGreeterHandingOverStaysUpAfterTheLogin(t *testing.T) {
	r := newRig(t, func(i *Init) { i.HandOver = true })
	r.s.prompt.Username.SetText("ada")
	r.s.submit("hunter2")
	r.conv(t).emit(auth.Event{Kind: auth.EventSuccess})
	if r.quit != 0 {
		t.Error("the greeter quit and its compositor would draw a frame without it")
	}
}

func TestGreeterShowsTheCardAgainWhenTheSessionDoesNotStart(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	r.s.prompt.Username.SetText("ada")
	r.s.submit("hunter2")
	r.s.leave(func() {})
	r.conv(t).emit(auth.Event{Kind: auth.EventFailure, Reason: "no session"})
	if !r.s.prompt.Reveal.Revealed() || !r.s.power.Visible() {
		t.Error("a session that failed to start left the login card hidden")
	}
}

// An X11 session is registered as one, or the desktop inside startx
// believes logind and looks for a Wayland compositor. Picked mid-login,
// its command and its type move together.
func TestAnX11SessionStartsWithItsOwnType(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("hunter2")
	r.s.drop.OnSelect(2)
	got := r.starts[0].Session()
	if !slices.Equal(got.Cmd, []string{"startx", "/usr/bin/env", "startplasma-x11"}) {
		t.Errorf("cmd = %v", got.Cmd)
	}
	if !slices.Equal(got.Env, []string{"XDG_SESSION_TYPE=x11", "LANG=C"}) {
		t.Errorf("env = %v", got.Env)
	}
}

func TestGreeterShowsNoRawReason(t *testing.T) {
	for _, refusal := range []auth.Refusal{auth.RefusalCredentials, auth.RefusalExpired, auth.RefusalTooManyAttempts, auth.RefusalDenied, auth.RefusalUnknown} {
		r := newRig(t, nil)
		r.s.prompt.Username.SetText("ada")
		r.s.submit("pw")
		r.conv(t).emit(auth.Event{Kind: auth.EventFailure, Reason: "pam_acct_mgmt: ACCT_EXPIRED", Refusal: refusal})
		if got, want := r.s.prompt.Error.Text(), loginscreen.RefusalText(refusal); got != want {
			t.Errorf("refusal %d shows %q, want %q", refusal, got, want)
		}
	}
}

func TestGreeterKeepsAModuleErrorOverTheRefusal(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("pw")
	first := r.conv(t)
	first.emit(auth.Event{Kind: auth.EventPrompt, Prompt: auth.Prompt{Kind: auth.PromptError, Text: "The account is locked"}})
	first.emit(auth.Event{Kind: auth.EventFailure, Refusal: auth.RefusalCredentials})
	if got := r.s.prompt.Error.Text(); got != "The account is locked" {
		t.Errorf("message = %q, want the module's", got)
	}
	r.s.submit("pw")
	second := r.conv(t)
	second.emit(auth.Event{Kind: auth.EventFailure, Refusal: auth.RefusalCredentials})
	if got, want := r.s.prompt.Error.Text(), loginscreen.RefusalText(auth.RefusalCredentials); got != want {
		t.Errorf("the next attempt = %q, want %q: the module error outlived its attempt", got, want)
	}
}

func TestGreeterInfoPromptDoesNotOutliveTheRefusal(t *testing.T) {
	r := newRig(t, nil)
	r.s.prompt.Username.SetText("ada")
	r.s.submit("pw")
	c := r.conv(t)
	c.emit(auth.Event{Kind: auth.EventPrompt, Prompt: auth.Prompt{Kind: auth.PromptInfo, Text: "Last login: never"}})
	c.emit(auth.Event{Kind: auth.EventFailure, Refusal: auth.RefusalCredentials})
	if got, want := r.s.prompt.Error.Text(), loginscreen.RefusalText(auth.RefusalCredentials); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestGreeterRevealsByTheConfiguredAnimation(t *testing.T) {
	zoom := config.AnimationZoom
	r := newRig(t, func(i *Init) { i.Config.Animations.Lock.Enter = &zoom })
	r.s.start()
	if got := r.s.prompt.Reveal.Transition(); got != widget.RevealZoom {
		t.Errorf("the card reveals by %v, want the configured zoom", got)
	}
	r = newRig(t, func(i *Init) {
		i.Config.Animations.Lock.Enter = &zoom
		i.Config.Animations.Enabled = false
	})
	r.s.start()
	if got := r.s.prompt.Reveal.Transition(); got != widget.RevealNone {
		t.Errorf("with animations off the card reveals by %v", got)
	}
}

func TestGreeterUsernameEntryOnlyWithoutAListedUser(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	if !r.s.prompt.Username.Visible() {
		t.Fatal("with nobody picked the entry takes the name")
	}
	r.s.users[0].button.OnClick()
	if r.s.prompt.Username.Visible() || r.s.prompt.Username.Text() != "ada" || r.focused != r.s.prompt.Entry {
		t.Error("a picked user takes the entry's place and fills it")
	}
	r.s.users[1].button.OnClick()
	if r.s.prompt.Username.Visible() || r.s.prompt.Username.Text() != "bob" || slices.Contains(r.s.users[0].button.Classes(), "selected") {
		t.Error("picking another user moves the pick")
	}
	r.s.users[1].button.OnClick()
	if !r.s.prompt.Username.Visible() || r.s.prompt.Username.Text() != "" || r.focused != r.s.prompt.Username || slices.Contains(r.s.users[1].button.Classes(), "selected") {
		t.Error("picking the user again clears the pick and hands the name back to the entry")
	}
	r.s.submit("hunter2")
	if len(r.convs) != 0 {
		t.Error("cleared, a login started without a name")
	}

	if r := newRig(t, func(i *Init) { i.LastUser = "ada" }); r.s.prompt.Username.Visible() {
		t.Error("a remembered listed user still shows the entry")
	}
	r = newRig(t, func(i *Init) { i.LastUser = "carol" })
	r.s.start()
	if !r.s.prompt.Username.Visible() || r.s.prompt.Username.Text() != "carol" {
		t.Error("a remembered user the list leaves out must stay in the entry")
	}
	r = newRig(t, func(i *Init) { i.LastUser = "ada"; i.Config.Login.Greeter.ShowUserList = false })
	if !r.s.prompt.Username.Visible() {
		t.Error("without the list the entry is the only way to name a user")
	}
}

func TestCoverOutputsEveryOutputAndLaterOnes(t *testing.T) {
	a, b, c := &app.Output{}, &app.Output{}, &app.Output{}
	var covered []*app.Output
	var added func(*app.Output)
	stopped := false
	stop := coverOutputs(true, []*app.Output{a, b}, func(add, _ func(*app.Output)) func() {
		added = add
		return func() { stopped = true }
	}, func(o *app.Output) { covered = append(covered, o) })
	if !slices.Equal(covered, []*app.Output{a, b}) {
		t.Fatalf("covered %d outputs of the 2 there", len(covered))
	}
	added(c)
	if !slices.Equal(covered, []*app.Output{a, b, c}) {
		t.Fatal("an output plugged in later stays uncovered")
	}
	stop()
	if !stopped {
		t.Error("the watch outlives the greeter")
	}
}

func TestCoverOutputsWithoutALayerShellLeavesThemToTheWindow(t *testing.T) {
	var covered int
	stop := coverOutputs(false, []*app.Output{{}}, func(_, _ func(*app.Output)) func() {
		t.Error("watching outputs nothing can cover")
		return func() {}
	}, func(*app.Output) { covered++ })
	stop()
	if covered != 0 {
		t.Errorf("covered %d outputs with no layer shell", covered)
	}
}

func TestCoverIsTheBackgroundAlone(t *testing.T) {
	r := newRig(t, nil)
	out := &app.Output{}
	cfg := r.s.coverOn(out, treetest.Font(t), nil)
	if cfg.Output != out || cfg.Layer != app.LayerBackground || cfg.Keyboard != app.KeyboardNone {
		t.Errorf("cover on %p layer %v keyboard %v", cfg.Output, cfg.Layer, cfg.Keyboard)
	}
	if want := app.AnchorTop | app.AnchorBottom | app.AnchorLeft | app.AnchorRight; cfg.Anchor != want {
		t.Errorf("anchor %v, want every edge", cfg.Anchor)
	}
	if len(treetest.WithClass(cfg.Root, "lock-bg")) != 1 {
		t.Error("the cover does not paint the background")
	}
	if len(treetest.All[*widget.Entry](cfg.Root)) != 0 || len(treetest.WithClass(cfg.Root, "lock-center")) != 0 {
		t.Error("a cover is no second form")
	}
}

func TestTheGreeterClockWakesOncePerMinute(t *testing.T) {
	r := newRig(t, nil)
	r.s.clock.Arm()
	if len(r.scheduled) != 1 || r.scheduled[0].d != time.Minute {
		t.Fatalf("scheduled %+v, want one wake for the next minute", r.scheduled)
	}
	r.scheduled[0].fn()
	if len(r.scheduled) != 2 {
		t.Errorf("%d wakes armed after one fired, want the next one", len(r.scheduled))
	}
}

func goroutineID() uint64 {
	buf := make([]byte, 64)
	n := runtime.Stack(buf, false)
	id, _, _ := strings.Cut(strings.TrimPrefix(string(buf[:n]), "goroutine "), " ")
	v, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		panic(err)
	}
	return v
}

func TestAPowerActionNeverRunsOnTheLoop(t *testing.T) {
	r := newRig(t, nil)
	loop := goroutineID()
	var onLoop atomic.Int32
	entered, release := make(chan struct{}, 2), make(chan struct{})
	r.onPower = func(string) {
		if goroutineID() == loop {
			onLoop.Add(1)
		}
		entered <- struct{}{}
		<-release
	}
	buttons := treetest.WithClass(r.s.power, "lock-power-button")
	if len(buttons) != 2 {
		t.Fatalf("%d power buttons", len(buttons))
	}
	for _, b := range buttons {
		btn, ok := b.(*widget.Button)
		if !ok {
			t.Fatalf("a power button is a %T", b)
		}
		btn.OnClick()
	}
	for range buttons {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("a power action never started while the click returned")
		}
	}
	close(release)
	r.powerVerbs(t, 2)
	if got := onLoop.Load(); got != 0 {
		t.Errorf("%d power actions on the loop's goroutine", got)
	}
}
