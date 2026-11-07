package keyboard_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stubbedev/lys-lib/xkb"

	"github.com/stubbedev/lys-login/internal/keyboard"
	"github.com/stubbedev/lys-login/internal/keyboard/keyboardtest"
)

func sources(t *testing.T, xorg, vconsole string) keyboard.Sources {
	t.Helper()
	dir := t.TempDir()
	s := keyboard.Sources{Timeout: 2 * time.Second, Xorg: filepath.Join(dir, "00-keyboard.conf"), Vconsole: filepath.Join(dir, "vconsole.conf")}
	for path, text := range map[string]string{s.Xorg: xorg, s.Vconsole: vconsole} {
		if text == "" {
			continue
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func noLocaledFailure(t *testing.T) func(error) {
	return func(err error) { t.Errorf("localed failed: %v", err) }
}

func TestLocaledWinsOverTheFiles(t *testing.T) {
	want := xkb.Names{Model: "pc105", Layout: "de,us", Variant: "nodeadkeys,", Options: "grp:alt_shift_toggle"}
	keyboardtest.ServeLocale1(t, want)
	r, err := sources(t, `Option "XkbLayout" "fr"`, "XKBLAYOUT=gb\n").Read(noLocaledFailure(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.Names != want || r.From != "org.freedesktop.locale1" {
		t.Errorf("read %+v from %s, want %+v from localed", r.Names, r.From, want)
	}
}

func TestAnEmptyLocaledAnswerIsTheSystemsAnswer(t *testing.T) {
	keyboardtest.ServeLocale1(t, xkb.Names{})
	r, err := sources(t, `Option "XkbLayout" "fr"`, "").Read(noLocaledFailure(t))
	if err != nil || r.Names != (xkb.Names{}) {
		t.Errorf("read %+v, %v; want no layout, as localed says", r.Names, err)
	}
}

func TestWithoutLocaledTheXorgFileComesFirst(t *testing.T) {
	keyboardtest.NoSystemBus(t)
	xorg := "# written by systemd-localed\nSection \"InputClass\"\n        Identifier \"system-keyboard\"\n        MatchIsKeyboard \"on\"\n        Option \"XkbLayout\" \"de,us\"\n        Option \"XkbModel\" \"pc105\"\n        Option \"XkbVariant\" \"nodeadkeys,\"\n        Option \"XkbOptions\" \"grp:alt_shift_toggle\"\nEndSection\n"
	var failed []error
	r, err := sources(t, xorg, "XKBLAYOUT=gb\n").Read(func(err error) { failed = append(failed, err) })
	if err != nil {
		t.Fatal(err)
	}
	if want := (xkb.Names{Model: "pc105", Layout: "de,us", Variant: "nodeadkeys,", Options: "grp:alt_shift_toggle"}); r.Names != want {
		t.Errorf("read %+v, want %+v", r.Names, want)
	}
	if len(failed) != 1 {
		t.Errorf("localed's failure was reported %d times, want once", len(failed))
	}
}

func TestWithoutLocaledOrXorgFileVconsoleConfCounts(t *testing.T) {
	keyboardtest.NoSystemBus(t)
	r, err := sources(t, "", "KEYMAP=de\nXKBLAYOUT=\"de\"\nXKBVARIANT='nodeadkeys'\n# XKBOPTIONS=ctrl:nocaps\n").Read(func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	if want := (xkb.Names{Layout: "de", Variant: "nodeadkeys"}); r.Names != want {
		t.Errorf("read %+v, want %+v", r.Names, want)
	}
}

func TestNothingConfiguredReadsNoLayout(t *testing.T) {
	keyboardtest.NoSystemBus(t)
	r, err := sources(t, "", "KEYMAP=us\n").Read(func(error) {})
	if err != nil || r.Names != (xkb.Names{}) {
		t.Errorf("read %+v, %v; want nothing", r.Names, err)
	}
}

func TestCheckRefusesWhatLysWMWouldNotStartWith(t *testing.T) {
	keyboardtest.XKBData(t)
	for _, ok := range []xkb.Names{{}, {Layout: "de"}, {Layout: "us,de", Variant: ",nodeadkeys", Options: "grp:alt_shift_toggle"}} {
		if err := keyboard.Check(ok); err != nil {
			t.Errorf("Check(%+v) = %v", ok, err)
		}
	}
	for _, bad := range []xkb.Names{{Layout: "de, us"}, {Layout: "de us"}, {Layout: "de", Variant: "a,b"}, {Model: "pc105,pc104"}, {Layout: "no-such-layout"}} {
		if err := keyboard.Check(bad); err == nil {
			t.Errorf("Check(%+v) passed", bad)
		}
	}
}
