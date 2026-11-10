// Package greeterconfig is what a user may change of the login screen
// the system shows: the login keys config.GreeterApplyKeys names and a
// background image, written to the runtime.yaml beside the greeter's
// system config, never to config.yaml itself.
//
// A Change is staged by the user who makes it, with that user's rights:
// Stage and NewChange read the background image, so no privileged
// process ever opens a path a user names. Check, run by whoever may
// write the system config (root, or lys-login's display manager), trusts
// nothing in a Change but the allowlisted keys, and Write takes only
// what Check passed.
package greeterconfig

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"

	"github.com/stubbedev/lys-desktop/config"
	"github.com/stubbedev/lys-lib/atomicfile"
)

// MaxImage bounds the background image a Change carries.
const MaxImage = 32 << 20

// MaxStaged bounds a Change's staged config text.
const MaxStaged = 1 << 20

var ErrInvalid = errors.New("invalid login screen change")

// Change is a staged change of the login screen.
type Change struct {
	// Staged is the staged config: a login table, or its keys at the top
	// level.
	Staged string `json:"staged"`
	// Image is the background an image-mode Staged names, read by the
	// user who staged it; nil for the other modes.
	Image *Image `json:"image,omitempty"`
}

// Image is a background image's bytes and its file extension.
type Image struct {
	// Ext is the extension, with its dot ("" without one).
	Ext  string `json:"ext"`
	Data []byte `json:"data"`
}

type Checked struct {
	login map[string]any
	image *Image
}

func Stage(path string) (Change, error) {
	text, err := readBounded(path, MaxStaged)
	if err != nil {
		return Change{}, fmt.Errorf("read the staged config: %w", err)
	}
	c, err := NewChange(string(text))
	if err != nil {
		return Change{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func NewChange(staged string) (Change, error) {
	c := Change{Staged: staged}
	login, err := c.login()
	if err != nil {
		return Change{}, err
	}
	src, ok := imagePath(login)
	if !ok {
		return c, nil
	}
	data, err := readBounded(src, MaxImage)
	if err != nil {
		return Change{}, fmt.Errorf("read the background image: %w", err)
	}
	c.Image = &Image{Ext: filepath.Ext(src), Data: data}
	return c, nil
}

func (c Change) Check() (Checked, error) {
	login, err := c.login()
	if err != nil {
		return Checked{}, err
	}
	if _, ok := imagePath(login); !ok {
		return Checked{login: login}, nil
	}
	switch {
	case c.Image == nil:
		return Checked{}, fmt.Errorf("%w: an image background without the image", ErrInvalid)
	case !imageExt.MatchString(c.Image.Ext):
		return Checked{}, fmt.Errorf("%w: background image extension %q is not a plain extension", ErrInvalid, c.Image.Ext)
	case len(c.Image.Data) > MaxImage:
		return Checked{}, fmt.Errorf("%w: the background image is over %d bytes", ErrInvalid, MaxImage)
	}
	return Checked{login: login, image: c.Image}, nil
}

func Write(c Checked, configPath string) (string, error) {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the greeter user reads the system config
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	login := maps.Clone(c.login)
	if c.image != nil {
		dest, err := c.image.write(dir)
		if err != nil {
			return "", err
		}
		login["background-image"] = dest
	}
	dest := config.RuntimeOverlayPath(configPath)
	root := map[string]any{}
	switch existing, err := os.ReadFile(dest); { //nolint:gosec // the system runtime overlay
	case err == nil:
		if root, err = config.ParseYAML(string(existing)); err != nil {
			return "", fmt.Errorf("existing %s is invalid: %w", dest, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("read %s: %w", dest, err)
	}
	root["login"] = login
	out, err := config.YAMLDocument(root)
	if err != nil {
		return "", fmt.Errorf("serialize %s: %w", dest, err)
	}
	if err := atomicfile.WriteFile(dest, []byte(out), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	return dest, nil
}

func (c Change) login() (map[string]any, error) {
	if len(c.Staged) > MaxStaged {
		return nil, fmt.Errorf("%w: the staged config is over %d bytes", ErrInvalid, MaxStaged)
	}
	table, err := config.ParseYAML(c.Staged)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid YAML: %w", ErrInvalid, err)
	}
	var source any = table
	if inner, ok := table["login"].(map[string]any); ok {
		source = inner
	}
	login := map[string]any{}
	for _, key := range config.GreeterApplyKeys {
		v, ok := config.PathValue(source, key)
		if !ok {
			continue
		}
		if err := config.InsertPath(login, key, v); err != nil {
			return nil, fmt.Errorf("%w: staged %s: %w", ErrInvalid, key, err)
		}
	}
	if len(login) == 0 {
		return nil, fmt.Errorf("%w: no recognised login screen keys", ErrInvalid)
	}
	return login, nil
}

func imagePath(login map[string]any) (string, bool) {
	if mode, _ := login["background-mode"].(string); mode != "image" {
		return "", false
	}
	src, _ := login["background-image"].(string)
	return src, src != ""
}

var imageExt = regexp.MustCompile(`^(\.[A-Za-z0-9]{1,8})?$`)

func (img Image) write(dir string) (string, error) {
	ext := img.Ext
	if ext == "" {
		ext = ".img"
	}
	dest := filepath.Join(dir, "greeter-background"+ext)
	if err := atomicfile.WriteFile(dest, img.Data, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", dest, err)
	}
	return dest, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // a path the caller names, read with its rights
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is over %d bytes", path, limit)
	}
	return data, nil
}
