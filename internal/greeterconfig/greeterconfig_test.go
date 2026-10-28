package greeterconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-shell/config"
)

func readRuntime(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := config.ParseYAML(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// stageWrite stages text as a file and writes the change for the
// config in dir.
func stageWrite(t *testing.T, dir, text string) (string, error) {
	t.Helper()
	staged := filepath.Join(dir, "staged.yaml")
	if err := os.WriteFile(staged, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Stage(staged)
	if err != nil {
		return "", err
	}
	return Write(c, filepath.Join(dir, "config.yaml"))
}

func TestWriteKeepsOnlyTheAllowlistedKeys(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "runtime.yaml"), []byte("styling:\n  theme: kept\n"), 0o600)
	dest, err := stageWrite(t, dir, "login:\n  background-color: \"#123456\"\n  evil: x\n  greeter:\n    show-user-list: false\n  lock:\n    pam-service: evil\ngeneral:\n  foo: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	out := readRuntime(t, dest)
	g := out["login"].(map[string]any)
	if g["background-color"] != "#123456" || g["evil"] != nil || out["general"] != nil || g["lock"] != nil ||
		g["greeter"].(map[string]any)["show-user-list"] != false {
		t.Errorf("runtime.yaml = %v", out)
	}
	if out["styling"].(map[string]any)["theme"] != "kept" {
		t.Error("other runtime.yaml tables must survive")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err == nil {
		t.Error("the write touched config.yaml")
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o644 {
		t.Errorf("runtime.yaml mode = %v, want world-readable", fi.Mode().Perm())
	}

	if _, err := stageWrite(t, dir, "general:\n  foo: 1\n"); err == nil {
		t.Error("a file without login screen keys applied")
	}
	dest, err = stageWrite(t, dir, "show-clock: false\n")
	if err != nil || readRuntime(t, dest)["login"].(map[string]any)["show-clock"] != false {
		t.Errorf("bare top-level keys: %v", err)
	}
	if _, err := stageWrite(t, dir, "[greeter]\nshow-clock = false\n"); err == nil || !strings.Contains(err.Error(), "invalid YAML") {
		t.Errorf("a TOML staged file: %v", err)
	}
}

// Image mode carries the image staged by the user and writes it next
// to the config; color mode leaves a home path alone and reads nothing.
func TestImageBackgrounds(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "wall.png")
	_ = os.WriteFile(src, []byte("PNGDATA"), 0o600)
	dest, err := stageWrite(t, dir, "login:\n  background-mode: image\n  background-image: \""+src+"\"\n")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "greeter-background.png")
	if got := readRuntime(t, dest)["login"].(map[string]any)["background-image"]; got != want {
		t.Errorf("background-image = %v, want %s", got, want)
	}
	if data, _ := os.ReadFile(want); string(data) != "PNGDATA" {
		t.Errorf("copied %q", data)
	}
	if fi, _ := os.Stat(want); fi.Mode().Perm() != 0o644 {
		t.Errorf("copy mode = %v, want world-readable", fi.Mode().Perm())
	}

	staged := filepath.Join(dir, "color.yaml")
	_ = os.WriteFile(staged, []byte("login:\n  background-mode: color\n  background-image: /nonexistent/x.png\n"), 0o600)
	c, err := Stage(staged)
	if err != nil || c.Image != nil {
		t.Fatalf("color mode: %+v, %v", c, err)
	}
	dest, err = Write(c, filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := readRuntime(t, dest)["login"].(map[string]any)["background-image"]; got != "/nonexistent/x.png" {
		t.Errorf("color mode rewrote the path: %v", got)
	}

	if _, err := stageWrite(t, dir, "login:\n  background-mode: image\n  background-image: /nonexistent/secret\n"); err == nil {
		t.Error("an image the stager cannot read was staged")
	}
}

// Write trusts nothing a Change carries beyond the allowlisted keys: an
// image background needs its image, and the image's extension cannot
// leave the config's directory.
func TestWriteRefusesAMalformedChange(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "etc", "config.yaml")
	image := "login:\n  background-mode: image\n  background-image: /home/u/w.png\n"
	for name, c := range map[string]Change{
		"no image":             {Staged: image},
		"a path as extension":  {Staged: image, Image: &Image{Ext: "/../../evil", Data: []byte("x")}},
		"an oversized image":   {Staged: image, Image: &Image{Ext: ".png", Data: make([]byte, MaxImage+1)}},
		"an oversized staging": {Staged: "show-clock: true\n" + strings.Repeat("#", MaxStaged)},
	} {
		if _, err := Write(c, cfg); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "etc")); len(entries) != 0 {
		t.Errorf("a refused change left %v", entries)
	}
	dest, err := Write(Change{Staged: image, Image: &Image{Data: []byte("x")}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := readRuntime(t, dest)["login"].(map[string]any)["background-image"]; got != filepath.Join(dir, "etc", "greeter-background.img") {
		t.Errorf("an image without an extension went to %v", got)
	}
}
