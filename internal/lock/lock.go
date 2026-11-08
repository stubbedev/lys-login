// Package lock is the login screen in its lock mode: an
// ext-session-lock surface per output, so the compositor blanks every
// output, routes input only here, and keeps the session locked if the
// shell dies. The password is verified by a PAM conversation on a
// worker goroutine (service/auth), so the event loop is never stalled.
//
// Triggers: logind's Lock and Unlock signals (loginctl lock-session,
// idle daemons), the dev.stubbe.lys.Shell1 Lock method (`lys-shell lock`,
// lys-lock), lock-on-start for autologin sessions, and the LockedHint
// restart recovery. The screen reports its state back with
// SetLockedHint.
package lock

import (
	"errors"
	"fmt"
	"image"
	"log"
	"slices"
	"strconv"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/i18n"
	"github.com/stubbedev/lys-desktop/imagedecode"
	"github.com/stubbedev/lys-desktop/shell/reveal"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/memory-go"

	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/fprint"
	"github.com/stubbedev/lys-login/internal/layouts"
	loginscreen "github.com/stubbedev/lys-login/internal/screen"
)

// blackout is the surface's clear color under the background, opaque
// from the first frame.
var blackout = render.RGB(0, 0, 0)

// locker is the session-lock surface the screen drives; appLocker
// adapts lys-ui's, tests substitute a fake.
type locker interface {
	// Available reports ext-session-lock-v1 support.
	Available() bool
	// Active reports a lock pending or held.
	Active() bool
	// Lock requests the session lock; cfg.Surface builds each output's
	// surface.
	Lock(cfg app.SessionLockConfig) error
	// Release unlocks a held lock or withdraws a pending one.
	Release()
	// Focus moves keyboard focus to w in the surface holding it.
	Focus(w widget.Widget)
}

// loop is the event loop the screen's timers and cross-goroutine
// events run on.
type loop interface {
	Invoke(func())
}

// Deps are the screen's collaborators; New fills the production ones.
type Deps struct {
	Locker locker
	Loop   loop
	// Now is the clock the grace window and the labels read.
	Now func() time.Time
	// Wall is the clock the screen shows.
	Wall loginscreen.WallClock
	// NewTimer makes a stopped Timer for a timer re-armed by every key.
	NewTimer func(fn func()) Timer
	// Auth starts a conversation; the production one is PAM against
	// the configured service for the session user.
	Auth func(service string, onEvent func(auth.Event)) authHandle
	// SetLockedHint reports the state to logind, best effort; it is called on
	// the loop, so it must not wait. nil for nothing.
	SetLockedHint func(locked bool)
	// Decode decodes the background image file, its pixels in mem
	// where it can.
	Decode func(mem *memory.Shared, path string) (imagedecode.Decoded, error)
	// Outputs are the device pixel sizes of the outputs a lock covers,
	// which the background is resampled to before it is blurred.
	Outputs func() []image.Point
	// Forget drops the renderer's copy of a raster that is no longer
	// drawn, before its pixels are freed.
	Forget func(render.ImageID)
	// OnLocked tells whoever waits that the compositor confirmed a lock; it is
	// called on the loop, so it must not wait. nil for nothing.
	OnLocked func(struct{})
	// OnCapsLock calls fn with Caps Lock's state on registering and on
	// each change, on the loop, until the stop it returns; nil for no warning.
	OnCapsLock func(fn func(on bool)) (stop func())
	// OnEnded runs on the loop when a lock ends: unlocked, refused, or
	// ended by the compositor; nil for nothing.
	OnEnded          func()
	Media            mediaSource
	Battery          func(onChange func(batteryLevel, bool)) (stop func())
	Fingerprint      func(onResult func(fprint.Result)) (stop func())
	OnKeyboardLayout func(fn func(app.KeyboardLayout)) (stop func())
	NextLayout       func(steps int)
	LabelLayouts     func(names []string) ([]layouts.Layout, error)
	SwitchUser       func()
}

// Timer runs the function it was made with on the loop, once per Reset. A firing
// already on its way when Reset or Stop comes may still arrive, so the function
// checks it is due.
type Timer interface {
	Reset(d time.Duration)
	Stop()
}

// authHandle is the running conversation's reply side.
type mediaSource interface {
	Watch(onChange func(nowPlaying, bool)) (stop func())
	PlayPause(bus string)
	Next(bus string)
}

type authHandle interface {
	Answer(string)
	Cancel()
}

// face is one output's lock surface: the account, the prompt and the
// blank scrim.
type face struct {
	account *loginscreen.AccountTile
	prompt  *loginscreen.Prompt
	scrim   *widget.Box
	media   *mediaRow
	battery *batteryChip
}

// Screen is the lock screen component. Every method runs on the event
// loop; auth events and signals cross into it through Loop.Invoke.
type Screen struct {
	cfg          config.LoginConfig
	chargingIcon string
	anims        config.AnimationsConfig
	wallpaper    string
	font         render.Font
	sheet        *widget.Stylesheet
	d            Deps
	// account is the session user's name as the screen shows it, picture
	// their avatar once it is decoded, nil until then.
	account string
	picture image.Image

	faces []*face
	// bg is the background made once per lock, shared by every
	// output's surface and freed when the lock ends; nil draws the
	// color. Its pixels live in mem.
	bg  *loginscreen.Backdrop
	mem memory.Shared
	// attempts counts failures in this lock session.
	attempts uint32
	// lockedAt drives the password-free grace window.
	lockedAt time.Time
	// auth is the running conversation, nil when idle; authGen tags
	// its events so a stale conversation's late events are dropped.
	auth    authHandle
	authGen uint64
	// moduleError is the last error a PAM module sent during this attempt,
	// which says more than the refusal that follows it.
	moduleError string
	// awaiting: a prompt is on screen waiting for the next submit.
	awaiting bool
	// pending answers the conversation's first prompt: the password
	// typed before PAM asked for it.
	pending   *string
	inputOn   bool
	message   string
	blanked   bool
	caps      bool
	watching  bool
	stops     []func()
	fpStop    func()
	layouts   *loginscreen.KeyboardLayouts
	media     nowPlaying
	mediaOK   bool
	battery   batteryLevel
	batteryOK bool
	blank     Timer
	blankDue  time.Time
	clock     *loginscreen.ClockTicker
	clockText string
	dateText  string
	// confirmed: the compositor sent locked for this lock.
	confirmed bool
	// unlocked: the last lock ended in an unlock, by password or by
	// logind.
	unlocked bool
	// leaving: an unlock is hiding the prompts before it releases the
	// lock; exiting holds the faces still hiding, and exitGen drops the
	// completions of an exit cancelled since.
	leaving bool
	exiting []*face
	exitGen uint64
}

// New builds the screen over its collaborators, asking account, the
// session user's name as the screen shows it, for the password.
// The prompt shapes text with font; sheet (the shell's stylesheet)
// styles the surfaces through the lock.css rules.
func New(cfg *config.Config, account string, font render.Font, sheet *widget.Stylesheet, d Deps) *Screen {
	s := &Screen{cfg: cfg.Login, chargingIcon: cfg.Battery.ChargingIcon, anims: cfg.Animations, wallpaper: cfg.Wallpaper.Wallpaper, account: account, font: font, sheet: sheet, d: d}
	var shown []func(time.Time) string
	if s.cfg.ShowClock {
		shown = append(shown, s.cfg.ClockFormat.Layout().Format, s.cfg.DateFormat.Layout().Format)
	}
	s.clock = loginscreen.NewClockTicker(d.Wall, s.refreshClock, shown...)
	s.layouts = loginscreen.NewKeyboardLayouts(loginscreen.LayoutDeps{
		Invoke: d.Loop.Invoke, Label: d.LabelLayouts, Next: d.NextLayout, Mode: "lock",
		Show: func([]layouts.Layout, uint32) {
			for _, f := range s.faces {
				s.layouts.ShowOn(f.prompt)
			}
		},
	})
	return s
}

// Locked reports whether a lock is pending or held.
func (s *Screen) Locked() bool { return s.d.Locker.Active() }

// Lock acquires the session lock and covers every output (idempotent
// while locked). Refused, with a log line, when [lock] is disabled or
// the compositor lacks ext-session-lock-v1.
func (s *Screen) Lock() error {
	if !s.cfg.Lock.Enabled {
		return errors.New("lock: disabled in config (login.lock.enabled)")
	}
	if s.leaving {
		s.cancelExit()
		return nil
	}
	if s.d.Locker.Active() {
		return nil
	}
	if !s.d.Locker.Available() {
		return errors.New("lock: the compositor does not support ext-session-lock-v1")
	}
	s.bg = s.loadBackground()
	s.attempts = 0
	s.resetAuth()
	s.lockedAt = s.d.Now()
	s.inputOn = true
	s.message = ""
	s.blanked = false
	s.confirmed = false
	s.clock.Start()
	err := s.d.Locker.Lock(app.SessionLockConfig{
		Surface:    s.buildFace,
		OnLocked:   s.onLocked,
		OnFinished: s.onFinished,
	})
	if err != nil {
		s.clock.Stop()
		s.faces = nil
		s.dropBackground()
		return fmt.Errorf("lock: cannot lock the session: %w", err)
	}
	s.startWatches()
	s.armBlank()
	return nil
}

func (s *Screen) showMedia() bool   { return s.cfg.Lock.ShowMedia && s.d.Media != nil }
func (s *Screen) showBattery() bool { return s.cfg.Lock.ShowBattery && s.d.Battery != nil }

func (s *Screen) startWatches() {
	if s.watching {
		return
	}
	s.watching = true
	if s.d.OnCapsLock != nil {
		s.stops = append(s.stops, s.d.OnCapsLock(s.setCaps))
	}
	if s.d.OnKeyboardLayout != nil {
		s.stops = append(s.stops, s.d.OnKeyboardLayout(s.layouts.Set))
	}
	if s.showMedia() {
		s.stops = append(s.stops, s.d.Media.Watch(s.setMedia))
	}
	if s.showBattery() {
		s.stops = append(s.stops, s.d.Battery(s.setBattery))
	}
	s.startFingerprint()
}

func (s *Screen) stopWatches() {
	for _, stop := range s.stops {
		stop()
	}
	s.stops = nil
	s.watching = false
	s.stopFingerprint()
}

func (s *Screen) startFingerprint() {
	if s.fpStop != nil || s.d.Fingerprint == nil || !s.cfg.Lock.Fingerprint || s.exhausted() {
		return
	}
	s.fpStop = s.d.Fingerprint(s.onFingerprint)
}

func (s *Screen) stopFingerprint() {
	if s.fpStop != nil {
		s.fpStop()
		s.fpStop = nil
	}
}

func (s *Screen) onFingerprint(r fprint.Result) {
	if s.fpStop == nil {
		return
	}
	s.Activity()
	switch r {
	case fprint.Match:
		log.Printf("lock: fingerprint matched")
		s.release()
	case fprint.NoMatch:
		s.countFailure()
		log.Printf("lock: fingerprint did not match (attempts %d)", s.attempts)
		if s.exhausted() {
			s.lockOut()
			return
		}
		s.setMessage(i18n.T("lock-fingerprint-no-match"))
	case fprint.Retry:
		s.setMessage(i18n.T("lock-fingerprint-retry"))
	}
}

func (s *Screen) countFailure() {
	if s.attempts < ^uint32(0) {
		s.attempts++
	}
}

func (s *Screen) lockOut() {
	s.stopFingerprint()
	s.resetAuth()
	s.setInput(false)
	s.setMessage(i18n.T("lock-locked-out"))
	s.clearEntries()
}

// loadBackground resolves the configured background and makes it at
// the outputs' size, blurred; nil draws the color.
func (s *Screen) loadBackground() *loginscreen.Backdrop {
	path := s.cfg.Background.ImagePath(s.wallpaper)
	if path == "" {
		return nil
	}
	bg, err := loginscreen.LoadBackdrop(&s.mem, s.d.Decode, path, s.cfg.Blur.Sigma(), s.d.Outputs())
	if err != nil {
		log.Printf("lock: %v; using the color fill", err)
		return nil
	}
	return bg
}

// dropBackground lets go of the backdrop once no surface draws it.
func (s *Screen) dropBackground() {
	if s.bg == nil {
		return
	}
	s.bg.Retire(s.d.Forget, "lock")
	s.bg = nil
}

// buildFace builds one output's surface; lys-ui calls it for every
// output at lock time and again for each output plugged in while
// locked. A new face picks up the current state.
func (s *Screen) buildFace(*app.Output) app.LockSurface {
	account := loginscreen.NewAccountTile(s.font, s.account)
	if s.picture != nil {
		account.SetPicture(s.picture)
	}
	f := &face{account: account}
	spec := loginscreen.Spec{
		Mode: loginscreen.Lock, Config: s.cfg, Font: s.font, Sheet: s.sheet, Backdrop: s.bg,
		Identity: account.Root, Password: i18n.T("lock-password"), CapsLock: i18n.T("lock-caps-lock"),
		OnSubmit: s.Submit,
	}
	if s.showMedia() {
		f.media = newMediaRow(s.font, s.playPause, s.nextTrack)
		f.media.set(s.media, s.mediaOK)
		spec.Lock.Status = f.media.root
	}
	if s.showBattery() {
		f.battery = newBatteryChip(s.font, s.chargingIcon)
		f.battery.set(s.battery, s.batteryOK)
		spec.Lock.Corner = f.battery.root
	}
	if s.d.SwitchUser != nil {
		spec.Lock.Footer = switchUserButton(s.font, s.d.SwitchUser)
	}
	if s.layouts.CanSwitch() {
		spec.NextLayout = s.layouts.Cycle
	}
	surface := loginscreen.Build(spec)
	f.prompt, f.scrim = surface.Prompt, surface.Scrim
	s.layouts.ShowOn(f.prompt)
	f.prompt.SetCapsLock(s.caps)
	f.prompt.Clock.SetText(s.clockText)
	f.prompt.Date.SetText(s.dateText)
	f.prompt.SetMessage(s.message)
	f.prompt.Entry.SetEnabled(s.inputOn)
	f.scrim.SetVisible(s.blanked)
	// The background is opaque at once; the prompt reveals over it.
	reveal.Show(f.prompt.Reveal, s.anims, config.AnimLock)
	s.faces = append(s.faces, f)
	return app.LockSurface{
		Root:       surface.Root,
		Background: blackout,
		Focus:      f.prompt.Entry,
		OnKey:      func(*widget.Router, uint32, app.Mods) { s.keyPressed() },
		OnClosed:   func() { s.dropFace(f) },
	}
}

// keyPressed is a press on any face: activity, which unblanks and re-arms
// the timer.
func (s *Screen) keyPressed() {
	s.Activity()
}

// dropFace forgets the surface of an unplugged output (or of an ended
// lock).
func (s *Screen) dropFace(f *face) {
	s.faces = slices.DeleteFunc(s.faces, func(cur *face) bool { return cur == f })
	if s.leaving {
		s.faceLeft(s.exitGen, f)
	}
}

func (s *Screen) onLocked() {
	s.confirmed = true
	s.setLockedHint(true)
	if s.d.OnLocked != nil {
		s.d.OnLocked(struct{}{})
	}
	log.Printf("lock: session locked (%d monitors)", len(s.faces))
}

// onFinished: the compositor refused the lock (a previous lock client
// still holds the session) or ended a held one by its own policy.
func (s *Screen) onFinished() {
	s.dropExit()
	if s.confirmed {
		log.Printf("lock: the compositor ended the session lock")
		s.setLockedHint(false)
	} else {
		log.Printf("lock: compositor denied the session lock (a previous lock client " +
			"may still hold the session); on Hyprland set " +
			"misc:allow_session_lock_restore = true to allow takeover")
	}
	s.confirmed = false
	s.stopTimers()
	s.resetAuth()
	s.faces = nil
	s.dropBackground()
	s.attempts = 0
	s.lockedAt = time.Time{}
	s.unlocked = false
	s.ended()
}

func (s *Screen) ended() {
	if s.d.OnEnded != nil {
		s.d.OnEnded()
	}
}

// ForceUnlock is logind's Unlock: tear down without a password - only
// something that already authorized it (logind, a PAM agent) sends it.
func (s *Screen) ForceUnlock() { s.release() }

// Submit handles one Enter in a password entry. The first submit of a
// lock session starts a PAM conversation and stashes the value for its
// first prompt; a submit while a later prompt is on screen (an expired
// password, an OTP) answers it.
func (s *Screen) Submit(value string) {
	if s.exhausted() {
		return
	}
	if s.awaiting {
		s.awaiting = false
		if s.auth != nil {
			s.auth.Answer(value)
		}
		s.setInput(false)
		return
	}
	if s.auth != nil {
		return // a conversation runs and asked nothing: a stray submit
	}
	if grace := s.cfg.Lock.GracePeriodMS; grace > 0 && !s.lockedAt.IsZero() &&
		s.d.Now().Sub(s.lockedAt) < time.Duration(grace)*time.Millisecond {
		log.Printf("lock: unlocked within grace window")
		s.release()
		return
	}
	if value == "" {
		return
	}
	s.setInput(false)
	s.startConversation(value)
}

func (s *Screen) startConversation(first string) {
	s.pending = &first
	s.authGen++
	gen := s.authGen
	s.auth = s.d.Auth(s.cfg.Lock.PamService, func(ev auth.Event) {
		s.d.Loop.Invoke(func() {
			if gen == s.authGen {
				s.onAuthEvent(ev)
			}
		})
	})
}

func (s *Screen) onAuthEvent(ev auth.Event) {
	switch ev.Kind {
	case auth.EventPrompt:
		s.onPrompt(ev.Prompt)
	case auth.EventSuccess:
		log.Printf("lock: authentication succeeded")
		s.release()
	case auth.EventFailure:
		s.onFailure(ev)
	}
}

// onPrompt answers input prompts from the stashed value when there is
// one, else re-enables the entry for the user; info and error prompts
// only show their text.
func (s *Screen) onPrompt(p auth.Prompt) {
	if !p.WantsInput() {
		if p.Kind == auth.PromptError {
			s.moduleError = p.Text
		}
		s.setMessage(p.Text)
		return
	}
	if s.pending != nil {
		answer := *s.pending
		s.pending = nil
		if s.auth != nil {
			s.auth.Answer(answer)
		}
		return
	}
	s.awaiting = true
	s.setInput(true)
	s.clearEntries()
	s.focusEntry()
}

// onFailure counts the attempt and re-arms the entry unless the cap is
// reached.
func (s *Screen) onFailure(ev auth.Event) {
	s.resetAuth()
	s.countFailure()
	log.Printf("lock: authentication failed (attempts %d): %s", s.attempts, ev.Reason)
	if s.exhausted() {
		s.stopFingerprint()
	}
	s.setInput(!s.exhausted())
	s.setMessage(s.failureText(ev.Refusal))
	s.moduleError = ""
	s.clearEntries()
	if !s.exhausted() {
		s.focusEntry()
	}
}

// failureText is show_error's message, from _lock.ftl.
func (s *Screen) failureText(r auth.Refusal) string {
	switch {
	case s.exhausted():
		return i18n.T("lock-locked-out")
	case s.moduleError != "":
		return s.moduleError
	case r != auth.RefusalCredentials:
		return loginscreen.RefusalText(r)
	case s.cfg.ShowFailedAttempts:
		return failedAttempts(s.attempts)
	}
	return i18n.T("lock-incorrect")
}

func failedAttempts(n uint32) string {
	return i18n.T("lock-failed-attempts", i18n.Str("count", strconv.FormatUint(uint64(n), 10)))
}

// exhausted reports the configured attempt cap reached (0 is no cap).
func (s *Screen) exhausted() bool {
	return s.cfg.MaxAttempts > 0 && s.attempts >= s.cfg.MaxAttempts
}

// resetAuth drops the running conversation (a blocked PAM prompt sees
// a cancellation) and its transient state.
func (s *Screen) resetAuth() {
	if s.auth != nil {
		s.auth.Cancel()
	}
	s.auth = nil
	s.authGen++
	s.awaiting = false
	s.pending = nil
}

// release unlocks: the prompts hide under the lock's exit transition,
// then the lock is released and the surfaces torn down. It is idempotent
// while the exit runs, and a Lock meanwhile cancels it.
func (s *Screen) release() {
	if s.leaving {
		return
	}
	s.leaving = true
	s.exitGen++
	gen := s.exitGen
	s.stopTimers()
	s.resetAuth()
	s.setInput(false)
	s.exiting = slices.Clone(s.faces)
	if len(s.exiting) == 0 {
		s.finishRelease()
		return
	}
	for _, f := range slices.Clone(s.exiting) {
		reveal.Hide(f.prompt.Reveal, s.anims, config.AnimLock, func() { s.faceLeft(gen, f) })
	}
}

func (s *Screen) faceLeft(gen uint64, f *face) {
	if !s.leaving || gen != s.exitGen {
		return
	}
	s.exiting = slices.DeleteFunc(s.exiting, func(cur *face) bool { return cur == f })
	if len(s.exiting) == 0 {
		s.finishRelease()
	}
}

func (s *Screen) dropExit() {
	s.leaving = false
	s.exiting = nil
	s.exitGen++
}

// cancelExit keeps the lock a re-lock asked for during the exit: the
// prompts come back and the watches run again.
func (s *Screen) cancelExit() {
	s.dropExit()
	s.lockedAt = s.d.Now()
	for _, f := range s.faces {
		reveal.Show(f.prompt.Reveal, s.anims, config.AnimLock)
	}
	s.setInput(true)
	s.clock.Start()
	s.startWatches()
	s.armBlank()
	s.focusEntry()
}

func (s *Screen) finishRelease() {
	s.dropExit()
	if s.d.Locker.Active() {
		s.d.Locker.Release()
	}
	s.confirmed = false
	s.faces = nil
	s.dropBackground()
	s.lockedAt = time.Time{}
	s.attempts = 0
	s.setLockedHint(false)
	s.unlocked = true
	log.Printf("lock: session unlocked")
	s.ended()
}

func (s *Screen) setLockedHint(locked bool) {
	if s.d.SetLockedHint != nil {
		s.d.SetLockedHint(locked)
	}
}

// Activity unblanks and re-arms the blank timer. Every key comes through here, so
// it allocates nothing.
func (s *Screen) Activity() {
	s.setBlanked(false)
	s.armBlank()
}

func (s *Screen) armBlank() {
	ms := s.cfg.Lock.BlankTimeoutMS
	if ms == 0 || !s.d.Locker.Active() {
		s.stopBlank()
		return
	}
	if s.blank == nil {
		s.blank = s.d.NewTimer(s.blankDueNow)
	}
	wait := time.Duration(ms) * time.Millisecond
	s.blankDue = s.d.Now().Add(wait)
	s.blank.Reset(wait)
}

func (s *Screen) stopBlank() {
	if s.blank != nil {
		s.blank.Stop()
	}
	s.blankDue = time.Time{}
}

// blankDueNow is the blank timer firing, unless a key since moved it on.
func (s *Screen) blankDueNow() {
	if s.blankDue.IsZero() || s.d.Now().Before(s.blankDue) {
		return
	}
	s.setBlanked(true)
}

func (s *Screen) setBlanked(on bool) {
	if s.blanked == on {
		return
	}
	s.blanked = on
	for _, f := range s.faces {
		f.scrim.SetVisible(on)
	}
	if on {
		s.clock.Stop()
		return
	}
	s.clock.Start()
}

// SetPicture shows img as the session user's avatar on every face, and
// on every face built after.
func (s *Screen) SetPicture(img image.Image) {
	s.picture = img
	for _, f := range s.faces {
		f.account.SetPicture(img)
	}
}

func (s *Screen) setMedia(np nowPlaying, ok bool) {
	if np == s.media && ok == s.mediaOK {
		return
	}
	s.media, s.mediaOK = np, ok
	for _, f := range s.faces {
		f.media.set(np, ok)
	}
}

func (s *Screen) playPause() {
	if s.mediaOK {
		s.d.Media.PlayPause(s.media.bus)
	}
}

func (s *Screen) nextTrack() {
	if s.mediaOK {
		s.d.Media.Next(s.media.bus)
	}
}

func (s *Screen) setBattery(l batteryLevel, ok bool) {
	if l == s.battery && ok == s.batteryOK {
		return
	}
	s.battery, s.batteryOK = l, ok
	for _, f := range s.faces {
		f.battery.set(l, ok)
	}
}

func (s *Screen) setCaps(on bool) {
	s.caps = on
	for _, f := range s.faces {
		f.prompt.SetCapsLock(on)
	}
}

func (s *Screen) refreshClock() {
	now := s.d.Now()
	s.clockText = s.cfg.ClockFormat.Layout().Format(now)
	s.dateText = s.cfg.DateFormat.Layout().Format(now)
	for _, f := range s.faces {
		f.prompt.Clock.SetText(s.clockText)
		f.prompt.Date.SetText(s.dateText)
	}
}

func (s *Screen) stopTimers() {
	s.clock.Stop()
	s.stopWatches()
	s.stopBlank()
	s.blanked = false
}

func (s *Screen) setInput(on bool) {
	s.inputOn = on
	for _, f := range s.faces {
		f.prompt.Entry.SetEnabled(on)
	}
}

func (s *Screen) setMessage(text string) {
	s.message = text
	for _, f := range s.faces {
		f.prompt.SetMessage(text)
	}
}

func (s *Screen) clearEntries() {
	for _, f := range s.faces {
		f.prompt.Entry.SetText("")
	}
}

// focusEntry puts keyboard focus back on every surface's entry (each
// surface keeps its own focus; the compositor picks the surface).
func (s *Screen) focusEntry() {
	for _, f := range s.faces {
		s.d.Locker.Focus(f.prompt.Entry)
	}
}
