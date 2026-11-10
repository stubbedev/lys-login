package dm

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-lib/logind/logindtest"
)

func TestLogindSessionCallsReachTheSystemBus(t *testing.T) {
	fake := logindtest.Start(t, logindtest.Config{Own: "c1", Display: "2"})
	fake.Bus.UseAsSystemBus(t)
	if err := lockLogindSession("2"); err != nil {
		t.Fatal(err)
	}
	if err := activateLogindSession("2"); err != nil {
		t.Fatal(err)
	}
	if got, want := fake.Methods(), []string{"Manager.LockSession", "Manager.ActivateSession"}; !slices.Equal(got, want) {
		t.Errorf("logind calls %v, want %v", got, want)
	}
	for _, c := range fake.Calls.Load() {
		if !slices.Equal(c.Args, []any{"2"}) {
			t.Errorf("%s args %v, want session 2", c.Method, c.Args)
		}
	}
	if err := lockLogindSession("9"); err == nil {
		t.Error("locking an unknown session succeeded")
	}
}
