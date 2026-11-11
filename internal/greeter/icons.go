package greeter

import (
	"embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sync"

	"github.com/stubbedev/lys-desktop/resources"
	"github.com/stubbedev/lys-lib/atomicfile"
	"github.com/stubbedev/lys-lib/xdg"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"

	loginscreen "github.com/stubbedev/lys-login/internal/screen"
)

// embeddedIcons holds the glyph the shared bundle does not.
//
//go:embed assets/*.svg
var embeddedIcons embed.FS

const (
	sleepIcon         = "ld-moon-symbolic"
	restartIcon       = "ld-rotate-ccw-symbolic"
	shutdownIcon      = "ld-power-symbolic"
	accessibilityIcon = "ld-accessibility-symbolic"
)

// themeIcons are the theme icons the greeter draws, each from the shared
// bundle or assets/<name>.svg: pre-login there is no icon theme to rely on.
func themeIcons() []string {
	return slices.Concat(buttonIcons[:], loginscreen.Icons())
}

var buttonIcons = [...]string{sleepIcon, restartIcon, shutdownIcon, accessibilityIcon}

func preloadButtonIcons(num, denom int) error {
	var errs []error
	for _, name := range buttonIcons {
		if err := widget.PreloadThemeIcon(name, powerIconPx, num, denom); err != nil {
			errs = append(errs, fmt.Errorf("greeter: preload the %s icon at %dpx for scale %d/%d: %w", name, powerIconPx, num, denom, err))
		}
	}
	return errors.Join(errs...)
}

func outputScales(outputs []*app.Output) []int {
	scales := make([]int, 0, len(outputs))
	for _, out := range outputs {
		scales = append(scales, out.Scale)
	}
	slices.Sort(scales)
	return slices.Compact(scales)
}

type iconPreload struct {
	scales  []int
	pending sync.WaitGroup
}

func (p *iconPreload) start(decode func(num, denom int) error) {
	p.pending.Go(func() {
		for _, scale := range p.scales {
			if err := decode(scale, 1); err != nil {
				log.Print(err)
			}
		}
	})
}

func (p *iconPreload) wait() { p.pending.Wait() }

const iconsDirName = "lys-greeter-icons"

// iconsDir is where the glyphs are written:
// $XDG_RUNTIME_DIR/lys-greeter-icons. The greeter is a Wayland client,
// which has a runtime directory.
func iconsDir(env xdg.Env) (string, error) {
	dir, ok := env.RuntimeDir(iconsDirName)
	if !ok {
		return "", fmt.Errorf("greeter: icon dir: XDG_RUNTIME_DIR=%q is not an absolute path", env("XDG_RUNTIME_DIR"))
	}
	return dir, nil
}

// installIcons is install_icons: the embedded glyphs written to
// iconsDir and that directory put first on the icon search path, so the
// widgets load them by name and recolor them as symbolic.
func installIcons() error {
	dir, err := iconsDir(xdg.OS)
	if err != nil {
		return err
	}
	if err := writeIcons(dir); err != nil {
		return err
	}
	widget.SetIconSearchPaths(append([]string{dir}, widget.IconSearchPaths()...))
	widget.RefreshIcons()
	return nil
}

func iconSVG(name, file string) ([]byte, error) {
	if name == accessibilityIcon {
		return embeddedIcons.ReadFile("assets/" + file)
	}
	return resources.Icons.ReadFile(path.Join(resources.IconsDir, file))
}

// writeIcons materializes the glyph of every theme icon into dir.
func writeIcons(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("greeter: icon dir %s: %w", dir, err)
	}
	for _, name := range themeIcons() {
		file := name + ".svg"
		data, err := iconSVG(name, file)
		if err != nil {
			return fmt.Errorf("greeter: embedded icon %s: %w", name, err)
		}
		if err := atomicfile.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			return fmt.Errorf("greeter: icon %s: %w", file, err)
		}
	}
	return nil
}
