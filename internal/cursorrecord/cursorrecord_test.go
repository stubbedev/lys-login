package cursorrecord

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func TestFromEnv(t *testing.T) {
	for _, c := range []struct {
		name string
		vars map[string]string
		want Cursor
		ok   bool
	}{
		{"theme and size", map[string]string{"XCURSOR_THEME": "Bibata", "XCURSOR_SIZE": "32"}, Cursor{"Bibata", 32}, true},
		{"Hyprland's size", map[string]string{"XCURSOR_THEME": "Adwaita", "HYPRCURSOR_SIZE": "28"}, Cursor{"Adwaita", 28}, true},
		{"XCURSOR_SIZE over Hyprland's", map[string]string{"XCURSOR_SIZE": "24", "HYPRCURSOR_SIZE": "28"}, Cursor{Size: 24}, true},
		{"a size that is no number", map[string]string{"XCURSOR_THEME": "Adwaita", "XCURSOR_SIZE": "big"}, Cursor{Theme: "Adwaita"}, true},
		{"nothing", map[string]string{"HOME": "/home/ada"}, Cursor{}, false},
	} {
		got, ok, err := FromEnv(env(c.vars))
		if err != nil || ok != c.ok || got != c.want {
			t.Errorf("%s: FromEnv = %+v, %v, %v; want %+v, %v", c.name, got, ok, err, c.want, c.ok)
		}
	}
}

func TestFromEnvRefusesAThemeThatIsNoDirectoryName(t *testing.T) {
	for _, theme := range []string{"../../etc", "a/b", "..", "line\nbreak"} {
		if got, ok, err := FromEnv(env(map[string]string{"XCURSOR_THEME": theme})); err == nil || ok {
			t.Errorf("XCURSOR_THEME=%q: FromEnv = %+v, %v, %v; want an error", theme, got, ok, err)
		}
	}
}

// What the display manager writes for a uid, every account reads back
// for that uid, and for no other.
func TestWriteIsReadBack(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cursor")
	p, err := Write(dir, 1000, Cursor{"Bibata", 32})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the record is %v, %v; want it readable by every account", fi, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the records' directory is %v, %v; want it open to every account", fi, err)
	}
	if got, err := Read(dir, 1000); err != nil || got != (Cursor{"Bibata", 32}) {
		t.Errorf("Read = %+v, %v; want Bibata at 32", got, err)
	}
	if _, err := Write(dir, 1000, Cursor{Theme: "Adwaita"}); err != nil {
		t.Fatal(err)
	}
	if got, err := Read(dir, 1000); err != nil || got != (Cursor{Theme: "Adwaita"}) {
		t.Errorf("a second record = %+v, %v; want it to replace the first whole", got, err)
	}
	if _, err := Read(dir, 1001); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("another uid's record = %v, want none", err)
	}
}

func TestWriteRefusesWhatCannotBeRecorded(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []Cursor{{}, {Theme: "../x", Size: 24}} {
		if _, err := Write(dir, 1000, c); err == nil {
			t.Errorf("Write(%+v) succeeded", c)
		}
	}
	if _, err := Read(dir, 1000); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused record left %v", err)
	}
}

func TestDecodingChecksTheCursor(t *testing.T) {
	var c Cursor
	if err := json.Unmarshal([]byte(`{"theme":"Bibata","size":24}`), &c); err != nil || c != (Cursor{"Bibata", 24}) {
		t.Errorf("a good cursor = %+v, %v", c, err)
	}
	for _, bad := range []string{`{}`, `{"theme":"../../root"}`, `{"size":-1}`, `[]`} {
		var c Cursor
		if err := json.Unmarshal([]byte(bad), &c); err == nil {
			t.Errorf("%s decoded to %+v", bad, c)
		}
	}
}
