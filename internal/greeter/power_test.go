package greeter

import (
	"slices"
	"strings"
	"testing"

	"github.com/stubbedev/lys-desktop/shell/treetest"
	"github.com/stubbedev/lys-lib/logind"
	"github.com/stubbedev/lys-lib/logind/logindtest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-ui/widget"
)

func TestEachPowerButtonCallsItsLogindAction(t *testing.T) {
	fake := logindtest.Start(t, logindtest.Config{})
	fake.Bus.UseAsSystemBus(t)
	var power systemPower
	t.Cleanup(power.close)
	r := newRig(t, nil)
	r.onPower = power.press
	buttons := treetest.WithClass(r.s.power, "lock-power-button")
	want := []string{"Manager.Suspend", "Manager.Reboot", "Manager.PowerOff"}
	if len(buttons) != len(want) {
		t.Fatalf("%d power buttons, want sleep, restart and shut down", len(buttons))
	}
	for i, b := range buttons {
		btn, ok := b.(*widget.Button)
		if !ok {
			t.Fatalf("a power button is a %T", b)
		}
		btn.OnClick()
		r.powerActions(t, i+1)
		if got := fake.Methods(); !slices.Equal(got, want[:i+1]) {
			t.Fatalf("after pressing %s: logind calls %v, want %v", treetest.Labels(r.s.power)[i], got, want[:i+1])
		}
	}
	for _, c := range fake.Calls.Load() {
		if !slices.Equal(c.Args, []any{false}) {
			t.Errorf("%s(%v), want it not interactive", c.Method, c.Args)
		}
	}
}

func TestARefusedPowerActionNamesTheAction(t *testing.T) {
	fake := logindtest.Start(t, logindtest.Config{})
	fake.Bus.UseAsSystemBus(t)
	fake.Refuse("Manager.Reboot", dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"Access denied"}))
	var power systemPower
	t.Cleanup(power.close)
	err := power.run(logind.Reboot)
	if err == nil || !strings.HasPrefix(err.Error(), "power action reboot: ") || !strings.Contains(err.Error(), "Access denied") {
		t.Errorf("a refused reboot: %v, want it named with logind's reason", err)
	}
}
