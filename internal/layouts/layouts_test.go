package layouts_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stubbedev/lys-login/internal/layouts"
)

const lst = `! model
  pc105           Generic 105-key PC
! layout
  us              English (US)
  de              German
  fr              French
! variant
  intl            us: English (US, intl., with dead keys)
  nodeadkeys      de: German (no dead keys)
! option
  grp                  Switching to another layout
`

func rulesDir(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "rules"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules", "evdev.lst"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLayoutsAreLabeledByTheirCode(t *testing.T) {
	got, err := layouts.Labeled([]string{"German (no dead keys)", "English (US)", "French"}, []string{filepath.Join(t.TempDir(), "absent"), rulesDir(t, lst)})
	if err != nil {
		t.Fatal(err)
	}
	want := []layouts.Layout{{"DE", "German (no dead keys)"}, {"US", "English (US)"}, {"FR", "French"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Labeled = %v, want %v", got, want)
	}
}

func TestTwoVariantsOfOneLayoutStayApart(t *testing.T) {
	got, err := layouts.Labeled([]string{"English (US)", "English (US, intl., with dead keys)"}, []string{rulesDir(t, lst)})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Label != "US" || got[1].Label != "US2" {
		t.Errorf("Labeled = %v, want US and US2", got)
	}
}

func TestAnUnknownLayoutShowsItsNameAndSaysSo(t *testing.T) {
	got, err := layouts.Labeled([]string{"English (US)", "Klingon"}, []string{rulesDir(t, lst)})
	if err == nil {
		t.Error("an unknown layout was not reported")
	}
	if want := []layouts.Layout{{"US", "English (US)"}, {"Klingon", "Klingon"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("Labeled = %v, want %v", got, want)
	}
}

func TestOneLayoutReadsNothing(t *testing.T) {
	got, err := layouts.Labeled([]string{"German"}, []string{filepath.Join(t.TempDir(), "absent")})
	if err != nil || len(got) != 1 || got[0].Name != "German" {
		t.Errorf("Labeled = %v, %v", got, err)
	}
}

func TestTheSystemRulesNameTheUsualLayouts(t *testing.T) {
	matches, _ := filepath.Glob("/nix/store/*xkeyboard-config*/share/X11/xkb")
	if len(matches) == 0 {
		matches = []string{"/usr/share/X11/xkb"}
	}
	got, err := layouts.Labeled([]string{"English (US)", "German"}, matches[:1])
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Label != "US" || got[1].Label != "DE" {
		t.Errorf("Labeled = %v", got)
	}
}
