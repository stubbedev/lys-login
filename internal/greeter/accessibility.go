package greeter

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stubbedev/lys-desktop/shell/widgets"
	"github.com/stubbedev/lys-desktop/styling"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-lib/xkb"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/lys-ui/widget/css"
)

type A11yOption uint8

const (
	ScreenReader A11yOption = iota
	LargeText
	HighContrast
	StickyKeys
	SlowKeys
	a11yOptions
)

var a11yNames = [a11yOptions]string{"screen-reader", "large-text", "high-contrast", "sticky-keys", "slow-keys"}

var a11yLabels = [a11yOptions]string{"greeter-screen-reader", "greeter-large-text", "greeter-high-contrast", "greeter-sticky-keys", "greeter-slow-keys"}

func (o A11yOption) String() string { return a11yNames[o] }

type Accessibility [a11yOptions]bool

const (
	LargeTextScale = 1.5
	SlowKeysDelay  = 300 * time.Millisecond
)

func AccessibilityPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "accessibility")
}

func LoadAccessibility(statePath string) (Accessibility, error) {
	path := AccessibilityPath(statePath)
	data, err := os.ReadFile(path) //nolint:gosec // the greeter's own state file
	if errors.Is(err, fs.ErrNotExist) {
		return Accessibility{}, nil
	}
	if err != nil {
		return Accessibility{}, fmt.Errorf("greeter: read %s: %w", path, err)
	}
	return parseAccessibility(path, string(data))
}

func parseAccessibility(path, data string) (Accessibility, error) {
	var a Accessibility
	for line := range strings.Lines(data) {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		i := slices.Index(a11yNames[:], name)
		if i < 0 {
			return Accessibility{}, fmt.Errorf("greeter: %s names %q, not one of %v", path, name, a11yNames)
		}
		a[i] = true
	}
	return a, nil
}

func (a Accessibility) String() string {
	var b strings.Builder
	for o, on := range a {
		if on {
			b.WriteString(a11yNames[o] + "\n")
		}
	}
	return b.String()
}

func (a Accessibility) WMConfig() string {
	var b strings.Builder
	if a[StickyKeys] {
		b.WriteString("    sticky_keys: true\n")
	}
	if a[SlowKeys] {
		fmt.Fprintf(&b, "    slow_keys: %d\n", SlowKeysDelay.Milliseconds())
	}
	return b.String()
}

func wmAction(o A11yOption, on bool) string {
	switch o {
	case StickyKeys:
		if on {
			return "stickykeys on"
		}
		return "stickykeys off"
	case SlowKeys:
		if on {
			return fmt.Sprintf("slowkeys %d", SlowKeysDelay.Milliseconds())
		}
		return "slowkeys 0"
	}
	return ""
}

type a11yDeps struct {
	readerFound  bool
	startReader  func() (stop func())
	textScale    func(scale float64)
	highContrast func(on bool)
	dispatch     func(action string)
	popover      func(from *widget.Button, content, focus widget.Widget, onClosed func()) (close func())
}

type a11yMenu struct {
	button    *widget.Button
	rows      [a11yOptions]*widget.SwitchRow
	content   *widget.Box
	close     func()
	stopRead  func()
	state     Accessibility
	applying  bool
	available [a11yOptions]bool
	saveSeq   uint64
	saveMu    sync.Mutex
	savedSeq  uint64
}

func (s *screen) a11yButton(font render.Font) *widget.Box {
	th := widget.Current()
	m := &s.a11y
	m.available = [a11yOptions]bool{
		ScreenReader: s.d.a11y.readerFound,
		LargeText:    true,
		HighContrast: true,
		StickyKeys:   s.d.a11y.dispatch != nil,
		SlowKeys:     s.d.a11y.dispatch != nil,
	}
	m.button = widget.NewButton(widget.NewThemeIcon(accessibilityIcon, powerIconPx), 0, 0)
	m.button.AddClass("lock-a11y-button", css.Ghost, css.Icon, css.Large)
	m.button.SetAccessibleName(t("greeter-accessibility"))
	m.button.SetCursorName("pointer")
	m.button.OnClick = s.toggleA11yMenu
	px := widgets.TextPx(styling.Body, 1)
	m.content = widget.NewBox(widget.Column, th.Spacing/2, 0)
	m.content.AddClass("lock-a11y-menu")
	for o := range a11yOptions {
		row := widget.NewSwitchRow(font, px, t(a11yLabels[o]), "", func(on bool) { s.setA11y(o, on) })
		row.SetAccessibleName(t(a11yLabels[o]))
		row.Switch().SetAccessibleName(t(a11yLabels[o]))
		row.SetVisible(m.available[o])
		m.rows[o] = row
		m.content.Append(row, false)
	}
	name := widget.NewLabel(font, widgets.TextPx(styling.Caption, 1), t("greeter-accessibility"), 0)
	name.AddClass("lock-power-name")
	name.SetAlignment(render.AlignCenter)
	col := widget.NewBox(widget.Column, th.Spacing/2, 0)
	col.AppendAligned(m.button, false, widget.AlignCenter).Append(name, false)
	return col
}

func (s *screen) toggleA11yMenu() {
	m := &s.a11y
	if m.close != nil {
		m.close()
		return
	}
	var first widget.Widget
	for o := range a11yOptions {
		if m.available[o] {
			first = m.rows[o].Switch()
			break
		}
	}
	m.close = s.d.a11y.popover(m.button, m.content, first, func() { m.close = nil })
}

func (s *screen) applyA11y(a Accessibility) {
	m := &s.a11y
	m.applying = true
	for o := range a11yOptions {
		if a[o] && m.available[o] {
			m.rows[o].Set(true)
		}
	}
	m.applying = false
	for o := range a11yOptions {
		if m.state[o] {
			s.enactA11y(o, true)
		}
	}
}

func (s *screen) setA11y(o A11yOption, on bool) {
	m := &s.a11y
	if m.state[o] == on {
		return
	}
	m.state[o] = on
	log.Printf("greeter: accessibility: %s %s", o, onOff(on))
	if m.applying {
		return
	}
	s.enactA11y(o, on)
	state := m.state
	m.saveSeq++
	seq := m.saveSeq
	s.saving.Go(func() {
		m.saveMu.Lock()
		defer m.saveMu.Unlock()
		if seq < m.savedSeq {
			return
		}
		m.savedSeq = seq
		if err := s.d.saveLast(AccessibilityPath(s.init.StatePath), state.String()); err != nil {
			log.Printf("greeter: remember the accessibility settings: %v", err)
		}
	})
}

func (s *screen) enactA11y(o A11yOption, on bool) {
	d := s.d.a11y
	switch o {
	case ScreenReader:
		s.screenReader(on)
	case LargeText:
		scale := 1.0
		if on {
			scale = LargeTextScale
		}
		d.textScale(scale)
	case HighContrast:
		d.highContrast(on)
	case StickyKeys, SlowKeys:
		d.dispatch(wmAction(o, on))
	}
}

func (s *screen) screenReader(on bool) {
	m := &s.a11y
	if !on {
		if m.stopRead != nil {
			m.stopRead()
			m.stopRead = nil
		}
		return
	}
	if m.stopRead == nil {
		m.stopRead = s.d.a11y.startReader()
	}
}

func (s *screen) toggleScreenReader() {
	if !s.a11y.available[ScreenReader] {
		return
	}
	s.a11y.rows[ScreenReader].Set(!s.a11y.state[ScreenReader])
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

const chordMods = app.ModShift | app.ModCtrl | app.ModAlt | app.ModSuper

func (s *screen) captureKey(a app.Accel) bool {
	if a.Sym != xkb.Keys || a.Mods&chordMods != app.ModSuper|app.ModAlt {
		return false
	}
	s.toggleScreenReader()
	return true
}
