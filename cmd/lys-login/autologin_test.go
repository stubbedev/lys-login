package main

import (
	"slices"
	"testing"

	"github.com/stubbedev/lys-login/internal/greeter"
)

var offered = []greeter.Session{
	{ID: "niri", Name: "Niri", Exec: []string{"niri-session"}},
	{ID: "plasma-x11", Kind: greeter.X11, Name: "Plasma (X11)", Exec: []string{"startx", "startplasma-x11"}},
}

func TestAutologinPicksTheNamedTheLastOrTheFirstSession(t *testing.T) {
	for _, tc := range []struct {
		session, last string
		want          []string
	}{
		{"plasma-x11", "niri", []string{"startx", "startplasma-x11"}},
		{"", "plasma-x11", []string{"startx", "startplasma-x11"}},
		{"", "", []string{"niri-session"}},
		{"", "gone", []string{"niri-session"}},
	} {
		a, err := autologinTarget("alice", tc.session, offered, tc.last, []string{"LANG=C"})
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		if a.User != "alice" || !slices.Equal(a.Session.Cmd, tc.want) || !slices.Contains(a.Session.Env, "LANG=C") {
			t.Errorf("%+v: %+v", tc, a)
		}
	}
	if a, _ := autologinTarget("alice", "plasma-x11", offered, "", nil); !slices.Contains(a.Session.Env, "XDG_SESSION_TYPE=x11") {
		t.Errorf("an X11 session starts as %v", a.Session.Env)
	}
}

func TestAutologinRefusesWhatItCannotStart(t *testing.T) {
	if _, err := autologinTarget("alice", "sway", offered, "", nil); err == nil {
		t.Error("a session nobody offers was accepted")
	}
	if _, err := autologinTarget("", "niri", offered, "", nil); err == nil {
		t.Error("a session without a user was accepted")
	}
	if a, err := autologinTarget("", "", offered, "", nil); a != nil || err != nil {
		t.Errorf("no autologin: %+v, %v", a, err)
	}
}
