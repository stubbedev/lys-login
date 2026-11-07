package screen

import (
	"log"
	"slices"

	"github.com/stubbedev/lys-ui/app"

	"github.com/stubbedev/lys-login/internal/layouts"
)

type LayoutDeps struct {
	Invoke func(func())
	Label  func(names []string) ([]layouts.Layout, error)
	Next   func(steps int)
	Show   func(labeled []layouts.Layout, active uint32)
	Mode   string
}

type KeyboardLayouts struct {
	d        LayoutDeps
	keyboard app.KeyboardLayout
	labeled  []layouts.Layout
	pending  int
	want     string
	logged   string
}

func NewKeyboardLayouts(d LayoutDeps) *KeyboardLayouts {
	return &KeyboardLayouts{d: d}
}

func (k *KeyboardLayouts) CanSwitch() bool { return k.d.Next != nil }

func (k *KeyboardLayouts) Set(l app.KeyboardLayout) {
	switch {
	case !slices.Equal(l.Names, k.keyboard.Names):
		k.labeled, k.pending = nil, 0
		if len(l.Names) > 1 {
			k.label(l.Names)
		}
	case l.Active != k.keyboard.Active && k.pending > 0:
		k.pending--
	}
	k.keyboard = l
	k.show()
	k.applyWanted()
}

func (k *KeyboardLayouts) label(names []string) {
	go func() {
		labeled, err := k.d.Label(names)
		if err != nil {
			log.Printf("%s: %v", k.d.Mode, err)
		}
		k.d.Invoke(func() {
			if slices.Equal(names, k.keyboard.Names) {
				k.labeled = labeled
				k.show()
			}
		})
	}()
}

func (k *KeyboardLayouts) ShowOn(p *Prompt) { p.SetLayouts(k.labeled, k.keyboard.Active) }

func (k *KeyboardLayouts) show() {
	k.d.Show(k.labeled, k.keyboard.Active)
	name := k.Active()
	if name == "" || name == k.logged {
		return
	}
	k.logged = name
	log.Printf("%s: keyboard layout %q, %d of %d", k.d.Mode, name, k.keyboard.Active+1, len(k.keyboard.Names))
}

func (k *KeyboardLayouts) Active() string {
	if int(k.keyboard.Active) >= len(k.keyboard.Names) {
		return ""
	}
	return k.keyboard.Names[k.keyboard.Active]
}

func (k *KeyboardLayouts) Cycle() {
	if len(k.keyboard.Names) < 2 || k.d.Next == nil {
		return
	}
	k.pending++
	k.d.Next(1)
}

func (k *KeyboardLayouts) Want(name string) {
	k.want = name
	k.applyWanted()
}

func (k *KeyboardLayouts) applyWanted() {
	n := len(k.keyboard.Names)
	if k.want == "" || n == 0 {
		return
	}
	target := slices.Index(k.keyboard.Names, k.want)
	k.want = ""
	if target < 0 || k.d.Next == nil {
		return
	}
	current := (int(k.keyboard.Active) + k.pending) % n
	steps := (target - current + n) % n
	if steps == 0 {
		return
	}
	k.pending += steps
	k.d.Next(steps)
}
