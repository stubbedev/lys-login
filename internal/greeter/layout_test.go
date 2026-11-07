package greeter

import (
	"slices"
	"testing"
	"time"

	"github.com/stubbedev/lys-ui/app"

	"github.com/stubbedev/lys-login/internal/layouts"
)

var usDE = []string{"English (US)", "German"}

func (r *rig) queueInvokes() func() {
	queued := make(chan func(), 8)
	r.invoke = func(fn func()) { queued <- fn }
	return func() { (<-queued)() }
}

func TestTheChipShowsOnceTheLayoutsAreLabeled(t *testing.T) {
	r := newRig(t, nil)
	run := r.queueInvokes()
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE[:1]})
	if r.s.prompt.Layout.Visible() {
		t.Error("one layout shows the chip")
	}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE, Active: 1})
	if r.s.prompt.Layout.Visible() {
		t.Error("the chip shows before its labels")
	}
	run()
	if !r.s.prompt.Layout.Visible() || !slices.Contains(labelsOf(r), "GE") {
		t.Errorf("two layouts, labeled: chip shown %v, labels %v", r.s.prompt.Layout.Visible(), labelsOf(r))
	}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE[:1]})
	if r.s.prompt.Layout.Visible() {
		t.Error("a keymap of one layout left the chip up")
	}
}

func labelsOf(r *rig) []string {
	out := make([]string, len(r.s.labeled))
	for i, l := range r.s.labeled {
		out[i] = l.Label
	}
	return out
}

func TestLabelsForAnOlderKeymapAreDropped(t *testing.T) {
	r := newRig(t, nil)
	run := r.queueInvokes()
	label, older := r.s.d.labelLayouts, make(chan struct{})
	r.s.d.labelLayouts = func(names []string) ([]layouts.Layout, error) {
		if names[0] == usDE[0] {
			<-older
		}
		return label(names)
	}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE})
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: []string{"French", "German"}})
	run()
	close(older)
	run()
	if got := labelsOf(r); !slices.Equal(got, []string{"FR", "GE"}) {
		t.Errorf("labels = %v", got)
	}
}

func TestClickingTheChipSwitchesToTheNextLayout(t *testing.T) {
	r := newRig(t, nil)
	r.queueInvokes()
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE})
	r.s.prompt.Layout.OnClick()
	if !slices.Equal(r.steps, []int{1}) {
		t.Errorf("a click asked for %v steps", r.steps)
	}
}

func TestWithoutLysWMTheChipTakesNoClick(t *testing.T) {
	r := buildRig(t, nil, &rig{noLayoutSwitch: true, timeout: make(chan time.Time), scale: 1})
	r.queueInvokes()
	if r.s.prompt.Layout.OnClick != nil {
		t.Error("the chip takes clicks with nothing to switch layouts")
	}
	r.s.init.Layouts = map[string]string{"ada": "German"}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE})
	r.s.wantLayoutOf("ada")
}

func TestTheRememberedUsersLayoutComesFirst(t *testing.T) {
	r := newRig(t, func(i *Init) {
		i.LastUser = "ada"
		i.Layouts = map[string]string{"ada": "German", "bob": "English (US)"}
	})
	r.queueInvokes()
	r.s.start()
	if len(r.steps) != 0 {
		t.Fatalf("switched %v before any keymap", r.steps)
	}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE})
	if !slices.Equal(r.steps, []int{1}) {
		t.Fatalf("the remembered user's German took %v steps from US, want one", r.steps)
	}
	r.s.pickUser(r.s.users[1])
	if !slices.Equal(r.steps, []int{1, 1}) {
		t.Errorf("picking bob while ada's switch is under way took %v, want one more step back to US", r.steps)
	}
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE, Active: 1})
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE, Active: 0})
	r.s.pickUser(r.s.users[1])
	r.s.pickUser(r.s.users[1])
	if !slices.Equal(r.steps, []int{1, 1}) {
		t.Errorf("bob's US, already active, took %v", r.steps)
	}
}

func TestALayoutNoLongerConfiguredIsLeftAlone(t *testing.T) {
	r := newRig(t, func(i *Init) {
		i.LastUser = "ada"
		i.Layouts = map[string]string{"ada": "Dvorak"}
	})
	r.queueInvokes()
	r.s.start()
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE})
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: []string{"Dvorak", "German"}})
	if len(r.steps) != 0 {
		t.Errorf("switched %v for a layout the keymap did not have when the user was chosen", r.steps)
	}
}

func TestTheLoginRemembersTheLayoutItWasTypedIn(t *testing.T) {
	r := newRig(t, nil)
	r.queueInvokes()
	r.s.setKeyboardLayout(app.KeyboardLayout{Names: usDE, Active: 1})
	r.s.prompt.Username.SetText("ada")
	r.s.remember(r.s.loginToRemember())
	if got := LoadLayouts(r.s.init.StatePath); got["ada"] != "German" || len(got) != 1 {
		t.Errorf("remembered layouts = %v, want ada typing German", got)
	}
}

func TestAnAccountNameThatIsNoFileNameRemembersNoLayout(t *testing.T) {
	for _, user := range []string{"", ".", "..", "a/b", "a\x00b"} {
		if _, ok := LastLayoutPath("/var/lib/lys-greeter/last-session", user); ok {
			t.Errorf("%q names a layout file", user)
		}
	}
	if p, ok := LastLayoutPath("/var/lib/lys-greeter/last-session", "ada"); !ok || p != "/var/lib/lys-greeter/last-layout/ada" {
		t.Errorf("ada's layout file = %q, %v", p, ok)
	}
}

func TestNoRememberedLayoutsReadsNone(t *testing.T) {
	if got := LoadLayouts(t.TempDir() + "/last-session"); got != nil {
		t.Errorf("LoadLayouts = %v", got)
	}
}
