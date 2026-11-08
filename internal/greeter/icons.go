package greeter

import (
	"embed"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/stubbedev/lys-desktop/resources"
	"github.com/stubbedev/lys-lib/atomicfile"
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
	return append([]string{sleepIcon, restartIcon, shutdownIcon, accessibilityIcon}, loginscreen.Icons()...)
}

// installIcons is install_icons: the embedded glyphs written to
// $XDG_RUNTIME_DIR/lys-greeter-icons (the temp dir without one) and
// that directory put first on the icon search path, so the widgets load
// them by name and recolor them as symbolic.
func installIcons() error {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "lys-greeter-icons")
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
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // the session runtime dir, as install_icons uses it
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
