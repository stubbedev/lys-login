// Package keyboard reads the system's keyboard layout for the greeter's
// compositor: systemd-localed's X11 keyboard, else the files localed reads
// it from.
package keyboard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	dbus "github.com/stubbedev/lys-lib/third_party/godbus"
	"github.com/stubbedev/lys-lib/xkb"
)

const (
	locale1Name  = "org.freedesktop.locale1"
	locale1Path  = dbus.ObjectPath("/org/freedesktop/locale1")
	propsGetAll  = "org.freedesktop.DBus.Properties.GetAll"
	BusTimeout   = 2 * time.Second
	XorgConf     = "/etc/X11/xorg.conf.d/00-keyboard.conf"
	VconsoleConf = "/etc/vconsole.conf"
)

type Sources struct {
	Timeout        time.Duration
	Xorg, Vconsole string
}

var System = Sources{Timeout: BusTimeout, Xorg: XorgConf, Vconsole: VconsoleConf}

type Reading struct {
	Names xkb.Names
	From  string
}

func (s Sources) Read(localedFailed func(error)) (Reading, error) {
	names, err := fromLocaled(s.Timeout)
	if err == nil {
		return Reading{Names: names, From: locale1Name}, nil
	}
	localedFailed(err)
	var r Reading
	for _, f := range []struct {
		path  string
		parse func(string) xkb.Names
	}{{s.Xorg, parseXorg}, {s.Vconsole, parseVconsole}} {
		data, err := os.ReadFile(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return r, fmt.Errorf("keyboard: read %s: %w", f.path, err)
		}
		if names := f.parse(string(data)); names != (xkb.Names{}) {
			r.Names, r.From = names, f.path
			return r, nil
		}
	}
	return r, nil
}

func fromLocaled(timeout time.Duration) (xkb.Names, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return xkb.Names{}, fmt.Errorf("keyboard: connect to the system bus: %w", err)
	}
	defer func() { _ = conn.Close() }()
	var props map[string]dbus.Variant
	if err := conn.Object(locale1Name, locale1Path).CallWithContext(ctx, propsGetAll, 0, locale1Name).Store(&props); err != nil {
		return xkb.Names{}, fmt.Errorf("keyboard: ask %s for its properties: %w", locale1Name, err)
	}
	var names xkb.Names
	for key, dst := range map[string]*string{"X11Model": &names.Model, "X11Layout": &names.Layout, "X11Variant": &names.Variant, "X11Options": &names.Options} {
		v, ok := props[key]
		if !ok {
			return xkb.Names{}, fmt.Errorf("keyboard: %s has no %s", locale1Name, key)
		}
		if err := v.Store(dst); err != nil {
			return xkb.Names{}, fmt.Errorf("keyboard: %s's %s: %w", locale1Name, key, err)
		}
	}
	return names, nil
}

func parseXorg(text string) xkb.Names {
	var names xkb.Names
	for line := range strings.SplitSeq(text, "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := quoted(line)
		if len(fields) != 3 || !strings.EqualFold(fields[0], "Option") {
			continue
		}
		if dst := field(&names, strings.TrimPrefix(strings.ToLower(fields[1]), "xkb")); dst != nil {
			*dst = fields[2]
		}
	}
	return names
}

func quoted(line string) []string {
	var out []string
	for rest := strings.TrimSpace(line); rest != ""; rest = strings.TrimSpace(rest) {
		if rest[0] != '"' {
			word, after, _ := strings.Cut(rest, " ")
			out, rest = append(out, strings.TrimSpace(word)), after
			continue
		}
		value, after, ok := strings.Cut(rest[1:], `"`)
		if !ok {
			return nil
		}
		out, rest = append(out, value), after
	}
	return out
}

func parseVconsole(text string) xkb.Names {
	var names xkb.Names
	for line := range strings.SplitSeq(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		name, ok := strings.CutPrefix(strings.TrimSpace(key), "XKB")
		if !ok {
			continue
		}
		if dst := field(&names, strings.ToLower(name)); dst != nil {
			*dst = unquote(strings.TrimSpace(value))
		}
	}
	return names
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func field(names *xkb.Names, key string) *string {
	switch key {
	case "model":
		return &names.Model
	case "layout":
		return &names.Layout
	case "variant":
		return &names.Variant
	case "options":
		return &names.Options
	}
	return nil
}

func Check(names xkb.Names) error {
	for _, f := range []struct {
		key, value string
		list       bool
	}{{"model", names.Model, false}, {"layout", names.Layout, true}, {"variant", names.Variant, true}, {"options", names.Options, true}} {
		if err := checkItems(f.key, f.value, f.list); err != nil {
			return err
		}
	}
	if layouts, variants := strings.Count(names.Layout, ",")+1, strings.Count(names.Variant, ",")+1; names.Layout != "" && names.Variant != "" && variants > layouts {
		return fmt.Errorf("keyboard: %q names %d variants for %d layouts", names.Variant, variants, layouts)
	}
	if _, err := xkb.NewContextFlags(os.Getenv, xkb.ContextNoEnvironmentNames).NewKeymapFromNames(names); err != nil {
		return fmt.Errorf("keyboard: compile %+v: %w", names, err)
	}
	return nil
}

func checkItems(key, value string, list bool) error {
	if value == "" {
		return nil
	}
	items := []string{value}
	if list {
		items = strings.Split(value, ",")
	}
	for _, item := range items {
		if strings.ContainsFunc(item, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '"' || r == '\\' }) || (item == "" && key != "variant") {
			return fmt.Errorf("keyboard: %s %q is not a comma-separated list of XKB names", key, value)
		}
	}
	return nil
}
