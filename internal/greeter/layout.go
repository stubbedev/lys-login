package greeter

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stubbedev/lys-shell/service/lyswm"
	"github.com/stubbedev/lys-ui/app"
)

func lastLayoutDir(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "last-layout")
}

func LastLayoutPath(statePath, user string) (string, bool) {
	if user == "" || user == "." || user == ".." || strings.ContainsAny(user, "/\x00") {
		return "", false
	}
	return filepath.Join(lastLayoutDir(statePath), user), true
}

func LoadLayouts(statePath string) map[string]string {
	dir := lastLayoutDir(statePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("greeter: the layouts users last typed in: %v", err)
		}
		return nil
	}
	recorded := make(map[string]string, len(entries))
	for _, e := range entries {
		if layout := LoadLast(filepath.Join(dir, e.Name())); layout != "" {
			recorded[e.Name()] = layout
		}
	}
	return recorded
}

func dispatchNextLayout(steps int) {
	go func() {
		for range steps {
			if err := lyswm.Dispatch("nextlayout"); err != nil {
				log.Printf("greeter: switch the keyboard layout: %v", err)
				return
			}
		}
	}()
}

func (s *screen) setKeyboardLayout(l app.KeyboardLayout) {
	switch {
	case !slices.Equal(l.Names, s.keyboard.Names):
		s.labeled, s.pendingSteps = nil, 0
		if len(l.Names) > 1 {
			s.labelLayouts(l.Names)
		}
	case l.Active != s.keyboard.Active && s.pendingSteps > 0:
		s.pendingSteps--
	}
	s.keyboard = l
	s.showLayout()
	s.applyWantedLayout()
}

func (s *screen) labelLayouts(names []string) {
	go func() {
		labeled, err := s.d.labelLayouts(names)
		if err != nil {
			log.Printf("greeter: %v", err)
		}
		s.d.invoke(func() {
			if slices.Equal(names, s.keyboard.Names) {
				s.labeled = labeled
				s.showLayout()
			}
		})
	}()
}

func (s *screen) showLayout() {
	s.prompt.SetLayouts(s.labeled, s.keyboard.Active)
	name := s.activeLayout()
	if name == "" || name == s.logged {
		return
	}
	s.logged = name
	log.Printf("greeter: keyboard layout %q, %d of %d", name, s.keyboard.Active+1, len(s.keyboard.Names))
}

func (s *screen) activeLayout() string {
	if int(s.keyboard.Active) >= len(s.keyboard.Names) {
		return ""
	}
	return s.keyboard.Names[s.keyboard.Active]
}

func (s *screen) cycleLayout() {
	if len(s.keyboard.Names) < 2 {
		return
	}
	s.pendingSteps++
	s.d.nextLayout(1)
}

func (s *screen) wantLayoutOf(user string) {
	s.wantLayout = s.init.Layouts[user]
	s.applyWantedLayout()
}

func (s *screen) applyWantedLayout() {
	n := len(s.keyboard.Names)
	if s.wantLayout == "" || n == 0 {
		return
	}
	target := slices.Index(s.keyboard.Names, s.wantLayout)
	s.wantLayout = ""
	if target < 0 || s.d.nextLayout == nil {
		return
	}
	current := (int(s.keyboard.Active) + s.pendingSteps) % n
	steps := (target - current + n) % n
	if steps == 0 {
		return
	}
	s.pendingSteps += steps
	s.d.nextLayout(steps)
}

func layoutSwitch() func(steps int) {
	if lyswm.SocketPath() == "" {
		return nil
	}
	return dispatchNextLayout
}
