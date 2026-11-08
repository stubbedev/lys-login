package screen

import (
	"testing"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/shell/apptheme"
	"github.com/stubbedev/lys-desktop/shell/treetest"
	"github.com/stubbedev/lys-desktop/styling"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/widget"

	"github.com/stubbedev/lys-login/internal/layouts"
)

var usDE = []layouts.Layout{{Label: "US", Name: "English (US)"}, {Label: "DE", Name: "German"}}

func styledSurface(t *testing.T, mode Mode, next func()) *Surface {
	t.Helper()
	cfg := config.Defaults()
	palette := cfg.Styling.ActivePalette()
	theme, err := styling.PaletteFromHex(palette)
	if err != nil {
		t.Fatal(err)
	}
	s := Build(Spec{
		Mode: mode, Config: cfg.Login, Font: treetest.Font(t), Sheet: widget.NewStylesheet(styling.BundleCSS(cfg, palette, theme), apptheme.Priority),
		Identity: NewAccountTile(treetest.Font(t), "Ada").Root, NextLayout: next, OnSubmit: func(string) {},
	})
	s.Prompt.Reveal.SetRevealed(true)
	s.Prompt.Reveal.Finish()
	return s
}

func frame(root widget.Widget) ([]render.Rect, bool) {
	widget.SizeWithin(root, widget.Size{W: 1280, H: 800})
	root.Arrange(render.Rect{W: 1280, H: 800})
	return widget.CollectDamage(root)
}

func settle(t *testing.T, root widget.Widget) {
	t.Helper()
	for range 8 {
		if _, any := frame(root); !any {
			return
		}
	}
	t.Fatal("the screen never stopped asking for frames")
}

func TestTheLayoutChipShowsOnlyForSeveralLayouts(t *testing.T) {
	for _, mode := range []Mode{Greeter, Lock} {
		s := styledSurface(t, mode, nil)
		if s.Prompt.Layout.Visible() {
			t.Errorf("mode %d: the chip shows before any keymap", mode)
		}
		s.Prompt.SetLayouts(usDE[:1], 0)
		if s.Prompt.Layout.Visible() {
			t.Errorf("mode %d: one layout shows a chip", mode)
		}
		s.Prompt.SetLayouts(usDE, 1)
		if !s.Prompt.Layout.Visible() || s.Prompt.layoutText.Text() != "DE" {
			t.Errorf("mode %d: two layouts, the second active: chip shown %v reading %q", mode, s.Prompt.Layout.Visible(), s.Prompt.layoutText.Text())
		}
		s.Prompt.SetLayouts(nil, 0)
		if s.Prompt.Layout.Visible() {
			t.Errorf("mode %d: a dropped keymap leaves the chip up", mode)
		}
	}
}

func TestShowingOrSwitchingTheLayoutChipDamagesOnlyItInOneFrame(t *testing.T) {
	s := styledSurface(t, Greeter, func() {})
	settle(t, s.Root)

	s.Prompt.SetLayouts(usDE[:1], 0)
	if rects, any := frame(s.Root); any {
		t.Errorf("one layout drew %v", rects)
	}

	s.Prompt.SetLayouts(usDE, 0)
	rects, any := frame(s.Root)
	if !any {
		t.Fatal("showing the chip drew nothing")
	}
	chip := s.Prompt.Layout.Bounds()
	if chip.Empty() {
		t.Fatal("the chip has no bounds once shown")
	}
	if got := render.UnionAll(rects); got.Intersect(chip) != got {
		t.Errorf("showing damaged %v, beyond the chip %v", got, chip)
	}
	if rects, any := frame(s.Root); any {
		t.Errorf("showing the chip took a second frame: %v", rects)
	}

	s.Prompt.SetLayouts(usDE, 1)
	rects, any = frame(s.Root)
	if !any {
		t.Fatal("switching the layout drew nothing")
	}
	if got := render.UnionAll(rects); got.Intersect(chip) != got {
		t.Errorf("switching damaged %v, beyond the chip %v", got, chip)
	}
	if s.Prompt.Layout.Bounds() != chip {
		t.Errorf("switching moved the chip from %v to %v", chip, s.Prompt.Layout.Bounds())
	}
	if rects, any := frame(s.Root); any {
		t.Errorf("switching took a second frame: %v", rects)
	}

	s.Prompt.SetLayouts(usDE, 1)
	if rects, any := frame(s.Root); any {
		t.Errorf("the same layout again drew %v", rects)
	}
}

func TestClickingTheLayoutChipAsksForTheNextLayout(t *testing.T) {
	clicks := 0
	s := styledSurface(t, Greeter, func() { clicks++ })
	s.Prompt.SetLayouts(usDE, 0)
	settle(t, s.Root)
	b := s.Prompt.Layout.Bounds()
	s.Prompt.Layout.ClickAt(widget.Point{X: b.X + b.W/2, Y: b.Y + b.H/2})
	if clicks != 1 {
		t.Errorf("a click asked %d times", clicks)
	}
	if s.Prompt.Layout.OnClick == nil {
		t.Error("a chip that switches layouts is not clickable")
	}
	if styledSurface(t, Greeter, nil).Prompt.Layout.OnClick != nil {
		t.Error("a chip without a way to switch takes clicks")
	}
}
