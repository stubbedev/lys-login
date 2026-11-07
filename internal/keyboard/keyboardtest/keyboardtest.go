package keyboardtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stubbedev/lys-lib/dbustest"
	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/xkb"
)

type locale1 struct{ names xkb.Names }

func (l locale1) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != "org.freedesktop.locale1" {
		return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", []any{iface})
	}
	return map[string]dbus.Variant{
		"X11Model":   dbus.MakeVariant(l.names.Model),
		"X11Layout":  dbus.MakeVariant(l.names.Layout),
		"X11Variant": dbus.MakeVariant(l.names.Variant),
		"X11Options": dbus.MakeVariant(l.names.Options),
		"Locale":     dbus.MakeVariant([]string{"LANG=de_DE.UTF-8"}),
	}, nil
}

func ServeLocale1(t *testing.T, names xkb.Names) {
	t.Helper()
	bus := dbustest.Start(t)
	bus.UseAsSystemBus(t)
	conn := bus.Conn(t)
	if err := conn.Export(locale1{names}, "/org/freedesktop/locale1", "org.freedesktop.DBus.Properties"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.RequestName("org.freedesktop.locale1", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
}

func NoSystemBus(t *testing.T) {
	t.Helper()
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "absent"))
}

func XKBData(t *testing.T) {
	t.Helper()
	root := os.Getenv("XKB_CONFIG_ROOT")
	if root == "" {
		matches, _ := filepath.Glob("/nix/store/*xkeyboard-config*/share/X11/xkb")
		if len(matches) == 0 {
			matches, _ = filepath.Glob("/usr/share/X11/xkb")
		}
		if len(matches) == 0 {
			t.Fatal("no xkeyboard-config data: set XKB_CONFIG_ROOT")
		}
		root = matches[0]
	}
	t.Setenv("XKB_CONFIG_ROOT", root)
}
