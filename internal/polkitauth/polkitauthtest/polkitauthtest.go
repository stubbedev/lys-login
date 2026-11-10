// Package polkitauthtest is a fake polkitd Authority on a private bus
// that the test makes its system bus: it answers CheckAuthorization
// with the verdict the test sets and records every check and cancel.
package polkitauthtest

import (
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	"github.com/stubbedev/lys-lib/dbusx"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
)

type Result struct {
	IsAuthorized bool
	IsChallenge  bool
	Details      map[string]string
}

var (
	Authorized = Result{IsAuthorized: true, Details: map[string]string{}}
	Denied     = Result{Details: map[string]string{}}
	Challenge  = Result{IsChallenge: true, Details: map[string]string{}}
	Dismissed  = Result{Details: map[string]string{"polkit.dismissed": "true"}}
)

type Subject struct {
	Kind    string
	Details map[string]dbus.Variant
}

type Check struct {
	Subject        Subject
	Action         string
	Details        map[string]string
	Flags          uint32
	CancellationID string
}

type Authority struct {
	Checks    dbustest.Var[[]Check]
	Cancelled dbustest.Var[[]string]
	result    dbustest.Var[Result]
	hold      chan struct{}
}

func Start(t testing.TB, r Result) *Authority {
	t.Helper()
	return start(t, r, nil)
}

func StartHolding(t testing.TB) *Authority {
	t.Helper()
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	return start(t, Denied, hold)
}

func start(t testing.TB, r Result, hold chan struct{}) *Authority {
	t.Helper()
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	a := &Authority{hold: hold}
	a.result.Store(r)
	release, err := dbusx.Serve(bus.Conn(t), dbusx.Service{
		Name:      "org.freedesktop.PolicyKit1",
		Path:      "/org/freedesktop/PolicyKit1/Authority",
		Interface: "org.freedesktop.PolicyKit1.Authority",
		Methods:   authority{a},
	})
	if err != nil {
		t.Fatalf("polkitauthtest: %v", err)
	}
	t.Cleanup(release)
	return a
}

func (a *Authority) Answer(r Result) { a.result.Store(r) }

type authority struct{ a *Authority }

func (x authority) CheckAuthorization(subject Subject, action string, details map[string]string, flags uint32, id string) (Result, *dbus.Error) {
	x.a.Checks.Update(func(c []Check) []Check {
		return append(c, Check{Subject: subject, Action: action, Details: details, Flags: flags, CancellationID: id})
	})
	if x.a.hold != nil {
		<-x.a.hold
	}
	return x.a.result.Load(), nil
}

func (x authority) CancelCheckAuthorization(id string) *dbus.Error {
	x.a.Cancelled.Update(func(c []string) []string { return append(c, id) })
	return nil
}
