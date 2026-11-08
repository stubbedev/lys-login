package screen

import (
	"image"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-desktop/shell/apptheme"
	"github.com/stubbedev/lys-desktop/shell/treetest"
	"github.com/stubbedev/lys-desktop/shell/widgets"
	"github.com/stubbedev/lys-desktop/styling"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/lys-ui/widget/css"
)

// TestBuildShape pins the prompt: the secret entry masks and submits
// on Enter, the clock follows ShowClock, the error line starts hidden,
// and the greeter's username entry moves focus to the secret entry.
func TestBuildShape(t *testing.T) {
	var submitted []string
	var focused []widget.Widget
	p := newPrompt(Options{
		Font: treetest.Font(t), ShowClock: true, WithUsername: true,
		Focus: func(w widget.Widget) { focused = append(focused, w) },
	}, func(s string) { submitted = append(submitted, s) })
	if p.Entry.Echo() != widget.EchoPassword {
		t.Error("the secret entry must mask its text")
	}
	p.Entry.SetText("hunter2")
	p.Entry.KeyAction(widget.KeyEnter, 0)
	if len(submitted) != 1 || submitted[0] != "hunter2" {
		t.Errorf("submits = %q", submitted)
	}
	if p.Username == nil {
		t.Fatal("WithUsername built no username entry")
	}
	p.Username.KeyAction(widget.KeyEnter, 0)
	if len(focused) != 1 || focused[0] != p.Entry {
		t.Error("Enter in the username entry must move focus to the secret entry")
	}
	if !p.Clock.Visible() || p.Error.Visible() {
		t.Error("clock shown, error hidden at start")
	}
	p.SetMessage("Incorrect password")
	if !p.Error.Visible() || p.Error.Text() != "Incorrect password" {
		t.Error("SetMessage must show the text")
	}
	p.SetMessage("")
	if p.Error.Visible() {
		t.Error("an empty message hides the line")
	}

	lock := newPrompt(Options{Font: treetest.Font(t)}, func(string) {})
	if lock.Username != nil || lock.Clock.Visible() {
		t.Error("the lock-screen prompt has no username entry, and ShowClock false hides the clock")
	}
}

// TestBuildIsTheMacOSLayout pins the layout on the macOS lock screen's:
// the date over the clock at the top, then the account, the password
// pill with its placeholder and the error line at the bottom, and the
// footer under them; the clock a numeric heading at the poster scale of
// the theme's text size, the error line lys-ui's error text.
func TestBuildIsTheMacOSLayout(t *testing.T) {
	identity := widget.NewBox(widget.Column, 0, 0)
	footer := widget.NewBox(widget.Row, 0, 0)
	p := newPrompt(Options{
		Font: treetest.Font(t), ShowClock: true, Identity: identity, Footer: footer, Password: "Enter Password",
	}, func(string) {})
	rows := promptStack(t, p).Children()
	if len(rows) != 4 {
		t.Fatalf("%d rows over the background, want the clock, the gap, the credentials and the footer", len(rows))
	}
	clock, gap, credentials := rows[0].(*widget.Box), rows[1].(*widget.Box), rows[2].(*widget.Box)
	if !slices.Contains(clock.Classes(), "lock-time") || !slices.Equal(clock.Children(), []widget.Widget{p.Date, p.Clock}) {
		t.Error("the top row is the date over the clock")
	}
	if len(gap.Children()) != 0 {
		t.Error("an empty gap pushes the credentials to the bottom")
	}
	if !slices.Contains(credentials.Classes(), "lock-credentials") || credentials.Children()[0] != identity {
		t.Error("the credentials start with the account asked for")
	}
	for _, w := range []widget.Widget{p.Entry, p.Caps, p.Error} {
		if !treetest.Contains(credentials, w) {
			t.Errorf("%v is not among the credentials", w.(interface{ Classes() []string }).Classes())
		}
	}
	if rows[3] != footer {
		t.Error("the footer is the bottom row")
	}
	if p.Entry.Placeholder() != "Enter Password" {
		t.Errorf("password placeholder = %q", p.Entry.Placeholder())
	}
	text := widgets.TextPx(styling.Body, 1)
	if p.Clock.SizePx() != text*styling.Poster.Ratio() || !slices.Contains(p.Clock.Classes(), css.Heading) || !slices.Contains(p.Clock.Classes(), css.Numeric) {
		t.Errorf("clock = %vpx %v, want %vpx numeric heading", p.Clock.SizePx(), p.Clock.Classes(), text*styling.Poster.Ratio())
	}
	if p.Date.SizePx() != text*styling.Title.Ratio() || slices.Contains(p.Date.Classes(), css.Heading) {
		t.Errorf("date = %vpx %v, want %vpx, no heading", p.Date.SizePx(), p.Date.Classes(), text*styling.Title.Ratio())
	}
	if !slices.Contains(p.Error.Classes(), css.Error) || slices.Contains(p.Error.Classes(), css.Heading) {
		t.Errorf("error line classes = %v", p.Error.Classes())
	}
}

// Without an identity or a footer the credentials stand alone at the
// bottom.
func TestBuildWithoutIdentityOrFooter(t *testing.T) {
	p := newPrompt(Options{Font: treetest.Font(t)}, func(string) {})
	rows := promptStack(t, p).Children()
	if len(rows) != 3 {
		t.Fatalf("%d rows, want the clock, the gap and the credentials", len(rows))
	}
	if first := rows[2].(*widget.Box).Children()[0]; first != p.Entry {
		t.Errorf("the credentials start with %T, want the password entry", first)
	}
}

func TestBackground(t *testing.T) {
	c, _ := config.ParseHexColor("#102030")
	if got := HexFill(c); got != render.RGB(0x10, 0x20, 0x30) {
		t.Errorf("HexFill = %#x", uint32(got))
	}
	fill, ok := Background(nil, HexFill(c)).(*widget.DrawingArea)
	if !ok || !slices.Contains(fill.Classes(), "lock-bg") {
		t.Error("no image: the .lock-bg color fill")
	}
	over, ok := Background(&Backdrop{Image: render.NewStillImage(image.NewRGBA(image.Rect(0, 0, 1, 1)))}, 0).(*widget.Overlay)
	if !ok || len(treetest.WithClass(over, "lock-bg-scrim")) != 1 {
		t.Error("an image: picture under the .lock-bg-scrim")
	}
}

// The peek icon reveals the masked secret and masks it again.
func TestPeekTogglesTheEcho(t *testing.T) {
	p := newPrompt(Options{Font: treetest.Font(t)}, func(string) {})
	p.togglePeek()
	if !p.peeked {
		t.Fatal("the peek did not reveal")
	}
	p.togglePeek()
	if p.peeked {
		t.Error("a second peek left the secret shown")
	}
}

// The scrim starts hidden and is the .lock-scrim node.
func TestScrim(t *testing.T) {
	s := Scrim()
	if s.Visible() || !slices.Contains(s.Classes(), "lock-scrim") {
		t.Error("the scrim starts hidden as .lock-scrim")
	}
}

const peekIconSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16"/></svg>`

// A first click must find both peek icons decoded: with their files gone, a cache
// miss could only fail, and neither the toggle nor the paint after it may.
func TestThePeekIconsNeedNoDiskAfterThePromptIsBuilt(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{peekReveal, peekConceal} {
		if err := os.WriteFile(filepath.Join(dir, name+".svg"), []byte(peekIconSVG), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := widget.IconSearchPaths()
	widget.SetIconSearchPaths(append([]string{dir}, paths...))
	widget.RefreshIcons()
	t.Cleanup(func() {
		widget.SetIconSearchPaths(paths)
		widget.RefreshIcons()
	})
	removed := false
	for _, scale := range [][2]int{{2, 1}, {5, 4}, {3, 2}} {
		num, denom := scale[0], scale[1]
		p := newPrompt(Options{Font: treetest.Font(t)}, func(string) {})
		pix := make([]byte, 4*64*64)
		cv := render.NewScaled(pix, 4*64, 64, 64, num, denom)
		widget.PaintChild(cv, p.scaleWatch)
		deadline := time.Now().Add(5 * time.Second)
		for !widget.ThemeIconPreloaded(peekReveal, p.iconPx, num, denom) || !widget.ThemeIconPreloaded(peekConceal, p.iconPx, num, denom) {
			if removed {
				t.Fatalf("scale %d/%d: the peek icons are not decoded and their files are gone", num, denom)
			}
			if time.Now().After(deadline) {
				t.Fatalf("scale %d/%d: the peek icons were not decoded in 5s", num, denom)
			}
			time.Sleep(time.Millisecond)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			p.togglePeek()
			icon := p.Entry.TrailingIcon()
			if got := icon.Measure(widget.Row, widget.Unknown).Nat; got != p.iconPx {
				t.Errorf("after a click the icon measures %d across, want %d", got, p.iconPx)
			}
			icon.Arrange(render.Rect{W: 32, H: 32})
			icon.Paint(cv)
			if err := icon.Err(); err != nil {
				t.Errorf("scale %d/%d: painting %s after the click read the disk and missed: %v", num, denom, icon.Name(), err)
			}
		}
		if !slices.ContainsFunc(pix, func(b byte) bool { return b != 0 }) {
			t.Errorf("scale %d/%d: the peek icon painted nothing", num, denom)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{peekReveal, peekConceal} {
			if err := os.WriteFile(filepath.Join(dir, name+".svg"), []byte(peekIconSVG), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestThePromptSitsOnTheBackgroundWithNoCard(t *testing.T) {
	p := newPrompt(Options{Font: treetest.Font(t), ShowClock: true}, func(string) {})
	cfg := config.Defaults()
	palette := cfg.Styling.ActivePalette()
	theme, err := styling.PaletteFromHex(palette)
	if err != nil {
		t.Fatal(err)
	}
	Window(widget.NewStylesheet(styling.BundleCSS(cfg, palette, theme), apptheme.Priority), p.Reveal)
	card := treetest.WithClass(p.Reveal, "lock-credentials")[0]
	for _, glass := range []string{css.LiquidGlass, css.Glass} {
		if len(treetest.WithClass(card, glass)) != 0 {
			t.Errorf("the prompt column holds a %q surface", glass)
		}
	}
	if bg := widget.StyleBackground(card); bg.A() != 0 {
		t.Errorf("the prompt column paints background %#08x, want none", uint32(bg))
	}
}

func promptStack(t *testing.T, p *Prompt) *widget.Box {
	t.Helper()
	layers := p.Reveal.Child().(*widget.Overlay).Children()
	if len(layers) != 2 {
		t.Fatalf("the prompt reveals %d layers, want the screen's column under the corner", len(layers))
	}
	if corner := layers[1].(*widget.Box).Children(); corner[len(corner)-1] != p.Layout {
		t.Fatal("the corner does not end in the layout chip")
	}
	return layers[0].(*widget.Box)
}
