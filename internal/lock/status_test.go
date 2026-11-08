package lock

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/i18n"
	"github.com/stubbedev/lys-desktop/shell/treetest"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"

	"github.com/stubbedev/lys-login/internal/auth"
	"github.com/stubbedev/lys-login/internal/fprint"
	"github.com/stubbedev/lys-login/internal/layouts"
)

type fakeMedia struct {
	onChange func(nowPlaying, bool)
	watches  int
	stops    int
	calls    []string
}

func (m *fakeMedia) Watch(onChange func(nowPlaying, bool)) func() {
	m.watches++
	m.onChange = onChange
	return func() { m.stops++; m.onChange = nil }
}

func (m *fakeMedia) PlayPause(bus string) { m.calls = append(m.calls, "play-pause "+bus) }
func (m *fakeMedia) Next(bus string)      { m.calls = append(m.calls, "next "+bus) }

type fakeBattery struct {
	onChange func(batteryLevel, bool)
	stops    int
}

func (b *fakeBattery) watch(onChange func(batteryLevel, bool)) func() {
	b.onChange = onChange
	return func() { b.stops++; b.onChange = nil }
}

type fakeFingerprint struct {
	onResult func(fprint.Result)
	starts   int
	stops    int
}

func (f *fakeFingerprint) verify(onResult func(fprint.Result)) func() {
	f.starts++
	f.onResult = onResult
	return func() { f.stops++; f.onResult = nil }
}

func (f *fakeFingerprint) running() bool { return f.onResult != nil }

type fakeKeyboard struct {
	fn    func(app.KeyboardLayout)
	steps []int
}

func (k *fakeKeyboard) watch(fn func(app.KeyboardLayout)) func() {
	k.fn = fn
	return func() { k.fn = nil }
}

type statusRig struct {
	*harness
	media    *fakeMedia
	battery  *fakeBattery
	finger   *fakeFingerprint
	keyboard *fakeKeyboard
}

func newStatusRig(t *testing.T, edit func(*config.LoginConfig)) *statusRig {
	t.Helper()
	return newStatusRigWith(t, edit, nil)
}

func newStatusRigWith(t *testing.T, edit func(*config.LoginConfig), more func(*Deps)) *statusRig {
	t.Helper()
	r := &statusRig{media: &fakeMedia{}, battery: &fakeBattery{}, finger: &fakeFingerprint{}, keyboard: &fakeKeyboard{}}
	r.harness = newHarnessWith(t, edit, func(d *Deps) {
		d.Media = r.media
		d.Battery = r.battery.watch
		d.Fingerprint = r.finger.verify
		d.OnKeyboardLayout = r.keyboard.watch
		d.NextLayout = func(n int) { r.keyboard.steps = append(r.keyboard.steps, n) }
		d.LabelLayouts = func(names []string) ([]layouts.Layout, error) {
			out := make([]layouts.Layout, len(names))
			for i, name := range names {
				out[i] = layouts.Layout{Label: strings.ToUpper(name[:2]), Name: name}
			}
			return out, nil
		}
		if more != nil {
			more(d)
		}
	})
	return r
}

var track = nowPlaying{bus: "org.mpris.MediaPlayer2.fake", title: "Blue in Green", artist: "Miles Davis", playing: true, canNext: true}

func TestTheLockShowsTheTrackAndDrivesThePlayer(t *testing.T) {
	r := newStatusRig(t, nil)
	r.lock(t)
	r.eachFace(t, func(f *face) {
		if f.media.root.Visible() {
			t.Error("the media row shows before a player")
		}
	})
	r.media.onChange(track, true)
	r.eachFace(t, func(f *face) {
		if !f.media.root.Visible() || f.media.title.Text() != track.title || f.media.artist.Text() != track.artist {
			t.Errorf("media row shown %v reading %q by %q", f.media.root.Visible(), f.media.title.Text(), f.media.artist.Text())
		}
		if f.media.play.AccessibleName() != i18n.T("lock-media-pause") {
			t.Errorf("a playing track's button is named %q", f.media.play.AccessibleName())
		}
	})
	r.s.faces[1].media.play.OnClick()
	r.s.faces[0].media.next.OnClick()
	if want := []string{"play-pause " + track.bus, "next " + track.bus}; !slices.Equal(r.media.calls, want) {
		t.Errorf("player calls %v, want %v", r.media.calls, want)
	}
	r.locker.plug()
	if f := r.s.faces[len(r.s.faces)-1]; f.media.title.Text() != track.title {
		t.Error("an output plugged in while locked shows no track")
	}
	r.media.onChange(nowPlaying{}, false)
	r.eachFace(t, func(f *face) {
		if f.media.root.Visible() {
			t.Error("the media row stays once the player is gone")
		}
	})
	r.s.faces[0].media.play.OnClick()
	if len(r.media.calls) != 2 {
		t.Errorf("a click with no player reached %v", r.media.calls)
	}
}

func TestShowMediaOffWatchesNoPlayer(t *testing.T) {
	r := newStatusRig(t, func(l *config.LoginConfig) { l.Lock.ShowMedia = false })
	r.lock(t)
	if r.media.watches != 0 {
		t.Error("show-media off watched the players")
	}
	r.eachFace(t, func(f *face) {
		if f.media != nil {
			t.Error("show-media off built a media row")
		}
	})
}

func TestTheBatteryRowTracksChargeAndIsAbsentWithoutABattery(t *testing.T) {
	r := newStatusRig(t, nil)
	r.lock(t)
	r.battery.onChange(batteryLevel{percent: 81}, true)
	r.eachFace(t, func(f *face) {
		if !f.battery.root.Visible() || f.battery.label.Text() != percentText(81) || f.battery.charging.Visible() {
			t.Errorf("battery shown %v reading %q charging %v", f.battery.root.Visible(), f.battery.label.Text(), f.battery.charging.Visible())
		}
	})
	r.battery.onChange(batteryLevel{percent: 82, charging: true}, true)
	r.eachFace(t, func(f *face) {
		if f.battery.label.Text() != percentText(82) || !f.battery.charging.Visible() {
			t.Errorf("charging battery reads %q charging %v", f.battery.label.Text(), f.battery.charging.Visible())
		}
	})
	r.battery.onChange(batteryLevel{}, false)
	r.eachFace(t, func(f *face) {
		if f.battery.root.Visible() {
			t.Error("a machine without a battery shows the battery row")
		}
	})
	r.s.Submit("pw")
	r.conv(t).onEvent(auth.Event{Kind: auth.EventSuccess})
	r.finishExit()
	if r.battery.stops != 1 || r.media.stops != 1 {
		t.Errorf("the unlock stopped the battery %d and media %d times, want once each", r.battery.stops, r.media.stops)
	}
}

func percentText(n int) string { return i18n.T("lock-battery", i18n.Str("percent", strconv.Itoa(n))) }

func TestTheLayoutChipFollowsASwitchWhileLocked(t *testing.T) {
	queued := make(chan func(), 4)
	r := newStatusRigWith(t, nil, func(d *Deps) { d.Loop = invokeQueue(queued) })
	r.lock(t)
	r.keyboard.fn(app.KeyboardLayout{Names: []string{"German", "English (US)"}})
	(<-queued)()
	r.eachFace(t, func(f *face) {
		if !f.prompt.Layout.Visible() || f.prompt.Layout.AccessibleName() != "German" {
			t.Errorf("chip shown %v naming %q, want German", f.prompt.Layout.Visible(), f.prompt.Layout.AccessibleName())
		}
	})
	r.keyboard.fn(app.KeyboardLayout{Names: []string{"German", "English (US)"}, Active: 1})
	r.eachFace(t, func(f *face) {
		if f.prompt.Layout.AccessibleName() != "English (US)" {
			t.Errorf("after the switch the chip names %q", f.prompt.Layout.AccessibleName())
		}
	})
	r.s.faces[0].prompt.Layout.OnClick()
	if !slices.Equal(r.keyboard.steps, []int{1}) {
		t.Errorf("a click on the lock's chip asked for %v", r.keyboard.steps)
	}
}

type invokeQueue chan func()

func (q invokeQueue) Invoke(fn func()) { q <- fn }

func TestAFingerprintMatchUnlocksWithoutAPassword(t *testing.T) {
	r := newStatusRig(t, nil)
	r.lock(t)
	if r.finger.starts != 1 || !r.finger.running() {
		t.Fatalf("the lock started %d verifications", r.finger.starts)
	}
	r.finger.onResult(fprint.Match)
	r.finishExit()
	if r.locker.active || len(r.convs) != 0 {
		t.Errorf("a match: locked %v, %d password conversations", r.locker.active, len(r.convs))
	}
	if r.finger.stops != 1 {
		t.Errorf("the verification was stopped %d times", r.finger.stops)
	}
}

func TestAFingerprintThatDoesNotMatchCountsAsAnAttempt(t *testing.T) {
	r := newStatusRig(t, func(l *config.LoginConfig) { l.MaxAttempts = 2 })
	r.lock(t)
	r.finger.onResult(fprint.NoMatch)
	if r.s.attempts != 1 || r.s.message != i18n.T("lock-fingerprint-no-match") || !r.finger.running() {
		t.Errorf("after a no-match: attempts %d, message %q, still verifying %v", r.s.attempts, r.s.message, r.finger.running())
	}
	r.finger.onResult(fprint.Retry)
	if r.s.message != i18n.T("lock-fingerprint-retry") || r.s.attempts != 1 {
		t.Errorf("a retry: message %q, attempts %d", r.s.message, r.s.attempts)
	}
	r.finger.onResult(fprint.NoMatch)
	if !r.s.exhausted() || r.finger.running() || r.s.message != i18n.T("lock-locked-out") {
		t.Errorf("the second no-match: exhausted %v, verifying %v, message %q", r.s.exhausted(), r.finger.running(), r.s.message)
	}
	r.eachFace(t, func(f *face) {
		if f.prompt.Entry.Enabled() {
			t.Error("a locked-out screen takes a password")
		}
	})
	if !r.locker.active {
		t.Error("no-matches unlocked")
	}
}

func TestAPasswordTypedWhileVerifyingUnlocksAndStopsTheVerification(t *testing.T) {
	r := newStatusRig(t, nil)
	r.lock(t)
	r.s.Submit("hunter2")
	c := r.conv(t)
	if !r.finger.running() {
		t.Fatal("a password stopped the verification before it succeeded")
	}
	c.onEvent(auth.Event{Kind: auth.EventPrompt, Prompt: auth.Prompt{Kind: auth.PromptSecret}})
	c.onEvent(auth.Event{Kind: auth.EventSuccess})
	r.finishExit()
	if r.locker.active || r.finger.running() || r.finger.stops != 1 {
		t.Errorf("password unlock: locked %v, verifying %v, stops %d", r.locker.active, r.finger.running(), r.finger.stops)
	}
}

func TestPasswordFailuresUpToTheCapStopTheVerification(t *testing.T) {
	r := newStatusRig(t, func(l *config.LoginConfig) { l.MaxAttempts = 1 })
	r.lock(t)
	r.s.Submit("wrong")
	r.conv(t).onEvent(auth.Event{Kind: auth.EventFailure, Refusal: auth.RefusalCredentials})
	if r.finger.running() {
		t.Error("a locked-out screen still verifies fingerprints")
	}
}

func TestFingerprintOffOrAbsentChangesNothing(t *testing.T) {
	r := newStatusRig(t, func(l *config.LoginConfig) { l.Lock.Fingerprint = false })
	r.lock(t)
	if r.finger.starts != 0 {
		t.Error("fingerprint off started a verification")
	}
	h := newHarness(t, nil)
	h.lock(t)
	h.s.Submit("hunter2")
	h.conv(t).onEvent(auth.Event{Kind: auth.EventSuccess})
	h.finishExit()
	if h.locker.active {
		t.Error("without fprintd the password no longer unlocks")
	}
}

func frameOf(root widget.Widget) ([]render.Rect, bool) {
	widget.SizeWithin(root, widget.Size{W: 1280, H: 800})
	root.Arrange(render.Rect{W: 1280, H: 800})
	return widget.CollectDamage(root)
}

func TestAnIdleLockWithAPausedPlayerCommitsNoFrames(t *testing.T) {
	r := newStatusRig(t, func(l *config.LoginConfig) { l.ShowClock = false })
	r.lock(t)
	paused := track
	paused.playing = false
	r.media.onChange(paused, true)
	r.battery.onChange(batteryLevel{percent: 64}, true)
	root := r.locker.surfaces[0].Root
	for _, f := range r.s.faces {
		f.prompt.Reveal.Finish()
	}
	settled := false
	for range 8 {
		if _, any := frameOf(root); !any {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatal("the lock never stopped asking for frames")
	}
	frames := 0
	for range 60 {
		r.media.onChange(paused, true)
		r.battery.onChange(batteryLevel{percent: 64}, true)
		if _, any := frameOf(root); any {
			frames++
		}
	}
	if frames != 0 {
		t.Errorf("an idle lock with a paused player committed %d frames over 60 unchanged updates", frames)
	}
}

func TestSwitchUserAsksTheDisplayManagerAndKeepsTheLock(t *testing.T) {
	asked := 0
	r := newStatusRigWith(t, nil, func(d *Deps) { d.SwitchUser = func() { asked++ } })
	r.lock(t)
	buttons := treetestButtons(r.locker.surfaces[0].Root, "lock-switch-user")
	if len(buttons) != 1 {
		t.Fatalf("%d Switch user buttons on a face", len(buttons))
	}
	buttons[0].OnClick()
	if asked != 1 || !r.locker.active {
		t.Errorf("a click asked %d times, locked %v; want one ask and the lock held", asked, r.locker.active)
	}
	plain := newHarness(t, nil)
	plain.lock(t)
	if len(treetestButtons(plain.locker.surfaces[0].Root, "lock-switch-user")) != 0 {
		t.Error("without a display manager to ask the lock offers Switch user")
	}
}

func treetestButtons(root widget.Widget, class string) []*widget.Button {
	var out []*widget.Button
	for _, w := range treetest.WithClass(root, class) {
		if b, ok := w.(*widget.Button); ok {
			out = append(out, b)
		}
	}
	return out
}
