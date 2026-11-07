package greeter

import (
	"os"
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/xkb"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
)

type fakeA11y struct {
	readers   int
	stopped   int
	scales    []float64
	contrast  []bool
	actions   []string
	popovers  int
	content   widget.Widget
	focus     widget.Widget
	onClosed  func()
	closeHits int
}

func (f *fakeA11y) deps(reader, wm bool) a11yDeps {
	d := a11yDeps{
		readerFound: reader,
		startReader: func() func() {
			f.readers++
			return func() { f.stopped++ }
		},
		textScale:    func(scale float64) { f.scales = append(f.scales, scale) },
		highContrast: func(on bool) { f.contrast = append(f.contrast, on) },
		popover: func(_ *widget.Button, content, focus widget.Widget, onClosed func()) func() {
			f.popovers++
			f.content, f.focus, f.onClosed = content, focus, onClosed
			return func() { f.closeHits++; onClosed() }
		},
	}
	if wm {
		d.dispatch = func(action string) { f.actions = append(f.actions, action) }
	}
	return d
}

func (r *rig) openA11y(t *testing.T) *widget.Box {
	t.Helper()
	r.s.a11y.button.OnClick()
	if r.a11y.popovers != 1 {
		t.Fatalf("the accessibility button opened %d popovers", r.a11y.popovers)
	}
	return r.a11y.content.(*widget.Box)
}

func TestTheAccessibilityMenuIsNamedAndReachableByKeyboard(t *testing.T) {
	r := newRig(t, nil)
	if r.s.a11y.button.AccessibleName() != "Accessibility" {
		t.Errorf("the button is named %q", r.s.a11y.button.AccessibleName())
	}
	if !widget.Describe(r.s.a11y.button).Focusable {
		t.Error("the accessibility button takes no focus")
	}
	content := r.openA11y(t)
	if r.a11y.focus != r.s.a11y.rows[ScreenReader].Switch() {
		t.Errorf("the popover focuses %v, want the first switch", r.a11y.focus)
	}
	widget.SizeWithin(content, widget.Size{W: 400, H: 400})
	content.Arrange(render400)
	var names []string
	for _, st := range widget.DescribeTree(content) {
		if !st.Focusable {
			continue
		}
		if st.Name == "" {
			t.Errorf("a focusable %v in the menu has no accessible name", st.Role)
		}
		if st.TabStop {
			names = append(names, st.Name)
		}
	}
	want := []string{"Screen reader", "Large text", "High contrast", "Sticky keys", "Slow keys"}
	names = slices.Compact(names)
	if !slices.Equal(names, want) {
		t.Errorf("tab stops %v, want %v", names, want)
	}
	router := &widget.Router{Root: content}
	if err := router.SetFocus(r.s.a11y.rows[LargeText].Switch()); err != nil {
		t.Fatal(err)
	}
	router.Type(' ')
	if !r.s.a11y.state[LargeText] || !slices.Equal(r.a11y.scales, []float64{LargeTextScale}) {
		t.Errorf("Space on Large text: state %v, scales %v", r.s.a11y.state, r.a11y.scales)
	}
	r.s.a11y.button.OnClick()
	if r.a11y.closeHits != 1 || r.s.a11y.close != nil {
		t.Errorf("a second click: %d closes, still open %v", r.a11y.closeHits, r.s.a11y.close != nil)
	}
}

func TestEachToggleActsAndIsRemembered(t *testing.T) {
	r := newRig(t, nil)
	r.openA11y(t)
	for o := range a11yOptions {
		r.s.a11y.rows[o].Set(true)
	}
	if r.a11y.readers != 1 || !slices.Equal(r.a11y.scales, []float64{LargeTextScale}) || !slices.Equal(r.a11y.contrast, []bool{true}) {
		t.Errorf("on: readers %d scales %v contrast %v", r.a11y.readers, r.a11y.scales, r.a11y.contrast)
	}
	if want := []string{"stickykeys on", "slowkeys 300"}; !slices.Equal(r.a11y.actions, want) {
		t.Errorf("lys-wm actions %v, want %v", r.a11y.actions, want)
	}
	r.s.saving.Wait()
	got, err := LoadAccessibility(r.s.init.StatePath)
	if err != nil || got != (Accessibility{true, true, true, true, true}) {
		t.Fatalf("remembered %v, %v", got, err)
	}
	r.s.a11y.rows[LargeText].Set(false)
	r.s.a11y.rows[ScreenReader].Set(false)
	if r.a11y.stopped != 1 || r.a11y.scales[len(r.a11y.scales)-1] != 1 {
		t.Errorf("off: reader stops %d, scales %v", r.a11y.stopped, r.a11y.scales)
	}
	r.s.saving.Wait()
	if got, _ := LoadAccessibility(r.s.init.StatePath); got[LargeText] || got[ScreenReader] || !got[HighContrast] {
		t.Errorf("remembered after turning two off: %v", got)
	}
}

func TestARememberedChoiceAppliesAtTheNextGreeter(t *testing.T) {
	r := newRig(t, func(i *Init) { i.A11y = Accessibility{LargeText: true, HighContrast: true, ScreenReader: true} })
	r.s.start()
	if !r.s.a11y.rows[LargeText].On() || !slices.Equal(r.a11y.scales, []float64{LargeTextScale}) || !slices.Equal(r.a11y.contrast, []bool{true}) || r.a11y.readers != 1 {
		t.Errorf("a restart: scales %v contrast %v readers %d", r.a11y.scales, r.a11y.contrast, r.a11y.readers)
	}
	if len(r.a11y.actions) != 0 {
		t.Errorf("sticky and slow keys off sent %v", r.a11y.actions)
	}
	r.s.saving.Wait()
	if _, err := os.Stat(AccessibilityPath(r.s.init.StatePath)); err == nil {
		t.Error("applying the remembered choice wrote it again")
	}
	r.s.end()
	if r.a11y.stopped != 1 {
		t.Error("the greeter ending left the screen reader running")
	}
}

func TestEveryToggleOffStartsNothing(t *testing.T) {
	r := newRig(t, nil)
	r.s.start()
	if r.a11y.readers != 0 || len(r.a11y.scales) != 0 || len(r.a11y.contrast) != 0 || len(r.a11y.actions) != 0 {
		t.Errorf("all off started readers %d scales %v contrast %v actions %v", r.a11y.readers, r.a11y.scales, r.a11y.contrast, r.a11y.actions)
	}
}

func TestWithoutAScreenReaderItsRowIsAbsent(t *testing.T) {
	r := buildRig(t, nil, &rig{noReader: true, noLayoutSwitch: true, outputs: nil, scale: 1})
	content := r.openA11y(t)
	widget.SizeWithin(content, widget.Size{W: 400, H: 400})
	content.Arrange(render400)
	for _, st := range widget.DescribeTree(content) {
		if st.Name == "Screen reader" || st.Name == "Sticky keys" || st.Name == "Slow keys" {
			t.Errorf("%q shows without its program", st.Name)
		}
	}
	if r.a11y.focus != r.s.a11y.rows[LargeText].Switch() {
		t.Error("the popover does not focus the first row shown")
	}
	if !r.s.captureKey(superAltS) {
		t.Error("Super+Alt+S was not taken")
	}
	if r.a11y.readers != 0 {
		t.Error("Super+Alt+S started a missing screen reader")
	}
}

func TestSuperAltSTogglesTheScreenReader(t *testing.T) {
	r := newRig(t, nil)
	if !r.s.captureKey(superAltS) || r.a11y.readers != 1 {
		t.Fatalf("Super+Alt+S: readers %d", r.a11y.readers)
	}
	r.s.captureKey(superAltS)
	if r.a11y.stopped != 1 {
		t.Error("a second Super+Alt+S left the reader running")
	}
	if r.s.captureKey(superS) {
		t.Error("Super+S was taken")
	}
}

func TestARememberedFileWithAnUnknownNameIsAnError(t *testing.T) {
	if _, err := parseAccessibility("f", "large-text\nzoom\n"); err == nil {
		t.Error("an unknown option parsed")
	}
	a, err := parseAccessibility("f", "sticky-keys\n\nslow-keys\n")
	if err != nil || a != (Accessibility{StickyKeys: true, SlowKeys: true}) {
		t.Errorf("parsed %v, %v", a, err)
	}
	if got := a.WMConfig(); got != "    sticky_keys: true\n    slow_keys: 300\n" {
		t.Errorf("kiosk lines %q", got)
	}
}

var (
	render400 = render.Rect{W: 400, H: 400}
	superAltS = app.Accel{Sym: xkb.Keys, Mods: app.ModSuper | app.ModAlt}
	superS    = app.Accel{Sym: xkb.Keys, Mods: app.ModSuper}
)

func TestTheFormsFieldsAreNamedForAScreenReader(t *testing.T) {
	r := newRig(t, nil)
	for _, field := range []struct {
		w    widget.Widget
		want string
	}{{r.s.prompt.Username, "Username"}, {r.s.prompt.Entry, "Enter Password"}} {
		if got := widget.Describe(field.w).Name; got != field.want {
			t.Errorf("a field is named %q, want %q", got, field.want)
		}
	}
}
