// Package screen is the login screen: one screen the greeter shows
// before login and the lock screen shows over a locked session, built by
// the same code (Build) and styled by the same lock.css rules, so the two
// look and behave alike. A Mode adds only what genuinely differs: the
// greeter's user list, username entry, session picker and power buttons.
//
// The layout is the macOS lock screen's: the date over a large clock at
// the top, and at the bottom the account being asked for, the password
// pill under it, the caps lock warning and the error line, and under
// all of it the greeter's power buttons. A chip in the top right corner
// shows the keyboard layout when there are several.
package screen

import (
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-shell/shell/widgets"
	"github.com/stubbedev/lys-shell/styling"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/lys-ui/widget/css"

	"github.com/stubbedev/lys-login/internal/layouts"
)

// entryChars is the entries' width-chars.
const entryChars = 18

const layoutChars = 4

// The password entry's peek icons: the
// eye reveals masked text, the crossed eye masks it again.
const (
	peekReveal  = "ld-eye-symbolic"
	peekConceal = "ld-eye-off-symbolic"
)

// Options configure one prompt.
type Options struct {
	// Font is the face the labels and entries shape with; the
	// stylesheet sets their sizes and weights.
	Font render.Font
	// ShowClock shows the clock and date.
	ShowClock bool
	// WithUsername adds a visible username entry above the secret one
	// (the greeter; the lock screen authenticates the session user).
	WithUsername bool
	// Identity goes above the entries: the lock screen's account, the
	// greeter's user list. Extra goes between the caps lock warning and
	// the error line (the greeter's session picker), Footer at the
	// bottom of the screen under all of it (the greeter's power
	// buttons). Any may be nil.
	Identity, Extra, Footer widget.Widget
	// Password is the secret entry's placeholder, CapsLock the caps
	// lock warning's text.
	Password, CapsLock string
	// Focus moves keyboard focus; the username entry's Enter uses it
	// to move on to the secret entry.
	Focus      func(widget.Widget)
	NextLayout func()
}

// Prompt is a built credential prompt and the handles its screen
// drives.
type Prompt struct {
	// Reveal is the root: everything over the background, hidden until
	// the screen shows it (CredentialBox::reveal).
	Reveal *widget.Revealer
	// Username is the visible username entry; nil without
	// WithUsername.
	Username *widget.Entry
	// Entry is the secret entry; Enter submits it.
	Entry *widget.Entry
	// Clock and Date are hidden without ShowClock.
	Clock, Date *widget.Label
	// Caps is the caps lock warning, hidden until SetCapsLock shows it.
	Caps *widget.Label
	// Error is the error and info line, hidden until it has text.
	Error      *widget.Label
	Layout     *widget.Button
	layoutText *widget.Label
	peeked     bool
	iconPx     int
	// scaleWatch sees the device scale each frame paints at, which is fractional
	// where the compositor says so and known to nothing before the first paint.
	scaleWatch *widget.DrawingArea
	preloaded  [2]int
}

// newPrompt assembles a prompt (CredentialBox::build); onSubmit fires with
// the secret entry's text each time the user presses Enter in it.
func newPrompt(o Options, onSubmit func(string)) *Prompt {
	textPx := widgets.TextPx(styling.Body, 1)
	p := &Prompt{
		Clock: label(o.Font, textPx*styling.Poster.Ratio(), "lock-clock", css.Heading, css.Numeric),
		Date:  label(o.Font, textPx*styling.Title.Ratio(), "lock-date"),
		Entry: entry(o.Font, textPx, o.Password),
		Caps:  label(o.Font, textPx, "lock-caps", css.Warning),
		Error: label(o.Font, textPx, "lock-error", css.Error),
	}
	p.Caps.SetText(o.CapsLock)
	p.Caps.SetVisible(false)
	p.Clock.SetVisible(o.ShowClock)
	p.Date.SetVisible(o.ShowClock)
	p.Entry.AddClass("lock-secret")
	p.Entry.SetEcho(widget.EchoPassword)
	p.iconPx = int(textPx)
	p.Entry.SetTrailingIcon(peekReveal, textPx, p.togglePeek)
	p.scaleWatch = widget.NewDrawingArea(0, 0)
	p.scaleWatch.AddClass("lock-scale-watch")
	p.scaleWatch.OnDraw = func(cv *render.Canvas, _ render.Rect) { p.preloadAt(cv.DeviceScale()) }
	p.Entry.OnActivate = onSubmit
	p.Error.SetVisible(false)
	p.layoutText = label(o.Font, textPx)
	p.layoutText.SetWidthChars(layoutChars)
	p.Layout = widget.NewButton(p.layoutText, 0, 0)
	p.Layout.AddClass("lock-layout", css.Pill, css.Ghost, css.Small)
	p.Layout.SetVisible(false)
	if o.NextLayout != nil {
		p.Layout.SetCursorName("pointer")
		p.Layout.OnClick = o.NextLayout
	}

	th := widget.Current()
	credentials := widget.NewBox(widget.Column, th.Spacing, 0)
	credentials.AddClass("lock-credentials")
	credentials.SetHAlign(widget.AlignCenter)
	if o.Identity != nil {
		credentials.AppendAligned(o.Identity, false, widget.AlignCenter)
	}
	if o.WithUsername {
		p.Username = entry(o.Font, textPx, "Username")
		p.Username.AddClass("lock-user")
		p.Username.OnActivate = func(string) {
			if o.Focus != nil {
				o.Focus(p.Entry)
			}
		}
		credentials.AppendAligned(p.Username, false, widget.AlignCenter)
	}
	credentials.AppendAligned(p.Entry, false, widget.AlignCenter).Append(p.Caps, false)
	if o.Extra != nil {
		credentials.AppendAligned(o.Extra, false, widget.AlignCenter)
	}
	credentials.Append(p.Error, false).Append(p.scaleWatch, false)

	clock := widget.NewBox(widget.Column, 0, 0)
	clock.AddClass("lock-time")
	clock.Append(p.Date, false).Append(p.Clock, false)
	stack := widget.NewBox(widget.Column, 2*th.Spacing, 0)
	stack.AddClass("lock-stack")
	stack.Append(clock, false).Append(widget.NewBox(widget.Column, 0, 0), true)
	stack.AppendAligned(credentials, false, widget.AlignCenter)
	if o.Footer != nil {
		stack.AppendAligned(o.Footer, false, widget.AlignCenter)
	}
	p.Layout.SetMargin(render.Insets{Top: 2 * th.Spacing, Right: 2 * th.Spacing})
	p.Reveal = widget.NewRevealer(widget.NewOverlay().Append(stack).AppendAligned(p.Layout, widget.AlignEnd, widget.AlignStart))
	return p
}

// entry is one of the prompt's pill entries, showing placeholder while
// empty.
func entry(font render.Font, px float64, placeholder string) *widget.Entry {
	e := widget.NewEntry(font, px, 0)
	e.AddClass("lock-entry")
	e.SetPlaceholder(placeholder)
	e.SetWidthChars(entryChars, 0)
	return e
}

// label is a label in the column: it fills the row, its text
// centered.
func label(font render.Font, px float64, classes ...string) *widget.Label {
	l := widget.NewLabel(font, px, "", 0)
	l.AddClass(classes...)
	l.SetAlignment(render.AlignCenter)
	return l
}

// preloadAt decodes both peek icons for the device scale num/denom the first time
// a frame paints at it, since the first click would otherwise read and rasterize one
// on the UI loop. It reads the disk, so it runs off it.
func (p *Prompt) preloadAt(num, denom int) {
	if p.preloaded == [2]int{num, denom} {
		return
	}
	p.preloaded = [2]int{num, denom}
	px := p.iconPx
	go func() {
		for _, name := range [...]string{peekReveal, peekConceal} {
			_ = widget.PreloadThemeIcon(name, px, num, denom)
		}
	}()
}

// togglePeek is the peek icon's click: the masked text shows, or hides
// again.
func (p *Prompt) togglePeek() {
	p.peeked = !p.peeked
	p.Entry.EchoReveal(p.peeked)
	icon := peekReveal
	if p.peeked {
		icon = peekConceal
	}
	p.Entry.SetTrailingIcon(icon, float64(p.iconPx), p.togglePeek)
}

// SetCapsLock shows or hides the caps lock warning.
func (p *Prompt) SetCapsLock(on bool) {
	if p.Caps.Visible() != on {
		p.Caps.SetVisible(on)
	}
}

func (p *Prompt) SetLayouts(all []layouts.Layout, active uint32) {
	shown := len(all) > 1 && int(active) < len(all)
	if shown {
		p.layoutText.SetText(all[active].Label)
		p.Layout.SetAccessibleName(all[active].Name)
	}
	p.Layout.SetVisible(shown)
}

// SetMessage shows text on the error line, or hides it when empty.
func (p *Prompt) SetMessage(text string) {
	p.Error.SetText(text)
	p.Error.SetVisible(text != "")
}

// Window is a lock or greeter surface's root (the node the
// .lock-window rules select), styled by sheet, holding the layers
// bottom to top.
func Window(sheet *widget.Stylesheet, layers ...widget.Widget) widget.Widget {
	overlay := widget.NewOverlay()
	for _, l := range layers {
		overlay.Append(l)
	}
	root := widget.NewBox(widget.Column, 0, 0)
	root.SetElement("window")
	root.AddClass("background", "lock-window")
	if sheet != nil {
		root.AttachStylesheet(sheet)
	}
	root.Append(overlay, true)
	return root
}

// Scrim is the blank-screen blackout (.lock-scrim), hidden until the
// blank timer shows it.
func Scrim() *widget.Box {
	s := widget.NewBox(widget.Column, 0, 0)
	s.AddClass("lock-scrim")
	s.SetVisible(false)
	return s
}
