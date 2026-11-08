package screen

import (
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-desktop/shell/treetest"
	"github.com/stubbedev/lys-ui/app"

	"github.com/stubbedev/lys-login/internal/layouts"
)

type layoutRig struct {
	k      *KeyboardLayouts
	p      *Prompt
	steps  []int
	queued chan func()
}

func newLayoutRig(t *testing.T, canSwitch bool) *layoutRig {
	t.Helper()
	r := &layoutRig{queued: make(chan func(), 8)}
	r.p = newPrompt(Options{Font: treetest.Font(t)}, func(string) {})
	d := LayoutDeps{
		Invoke: func(fn func()) { r.queued <- fn },
		Label: func(names []string) ([]layouts.Layout, error) {
			out := make([]layouts.Layout, len(names))
			for i, name := range names {
				out[i] = layouts.Layout{Label: strings.ToUpper(name[:2]), Name: name}
			}
			return out, nil
		},
		Show: func([]layouts.Layout, uint32) { r.k.ShowOn(r.p) },
		Mode: "test",
	}
	if canSwitch {
		d.Next = func(n int) { r.steps = append(r.steps, n) }
	}
	r.k = NewKeyboardLayouts(d)
	return r
}

func (r *layoutRig) run() { (<-r.queued)() }

var usDENames = []string{"English (US)", "German"}

func TestTheChipShowsOnceTheLayoutsAreLabeled(t *testing.T) {
	r := newLayoutRig(t, true)
	r.k.Set(app.KeyboardLayout{Names: usDENames[:1]})
	if r.p.Layout.Visible() {
		t.Error("one layout shows the chip")
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames, Active: 1})
	if r.p.Layout.Visible() {
		t.Error("the chip shows before its labels")
	}
	r.run()
	if !r.p.Layout.Visible() || r.p.layoutText.Text() != "GE" {
		t.Errorf("two layouts, labeled: chip shown %v reading %q", r.p.Layout.Visible(), r.p.layoutText.Text())
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames, Active: 0})
	if r.p.layoutText.Text() != "EN" {
		t.Errorf("a switch to US reads %q", r.p.layoutText.Text())
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames[:1]})
	if r.p.Layout.Visible() {
		t.Error("a keymap of one layout left the chip up")
	}
}

func TestLabelsForAnOlderKeymapAreDropped(t *testing.T) {
	r := newLayoutRig(t, true)
	label, older := r.k.d.Label, make(chan struct{})
	r.k.d.Label = func(names []string) ([]layouts.Layout, error) {
		if names[0] == usDENames[0] {
			<-older
		}
		return label(names)
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames})
	r.k.Set(app.KeyboardLayout{Names: []string{"French", "German"}})
	r.run()
	close(older)
	r.run()
	if r.p.layoutText.Text() != "FR" {
		t.Errorf("the chip reads %q, want the newer keymap's FR", r.p.layoutText.Text())
	}
}

func TestCyclingAsksForTheNextLayout(t *testing.T) {
	r := newLayoutRig(t, true)
	r.k.Cycle()
	if len(r.steps) != 0 {
		t.Errorf("cycling with no keymap asked for %v", r.steps)
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames})
	r.k.Cycle()
	if !slices.Equal(r.steps, []int{1}) {
		t.Errorf("cycling asked for %v steps", r.steps)
	}
}

func TestWithoutASwitcherNothingSwitches(t *testing.T) {
	r := newLayoutRig(t, false)
	if r.k.CanSwitch() {
		t.Error("a tracker without Next reports it can switch")
	}
	r.k.Set(app.KeyboardLayout{Names: usDENames})
	r.k.Cycle()
	r.k.Want("German")
}
