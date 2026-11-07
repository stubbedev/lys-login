// Package layouts names a keymap's layouts the short way a layout chip
// shows them, from the rules list of xkeyboard-config.
package layouts

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const rulesList = "rules/evdev.lst"

type Layout struct {
	Label, Name string
}

func Labeled(names []string, includePaths []string) ([]Layout, error) {
	out := make([]Layout, len(names))
	missing := make(map[string][]int, len(names))
	for i, name := range names {
		out[i] = Layout{Label: name, Name: name}
		missing[name] = append(missing[name], i)
	}
	if len(names) < 2 {
		return out, nil
	}
	for _, dir := range includePaths {
		path := filepath.Join(dir, rulesList)
		f, err := os.Open(path) //nolint:gosec // xkeyboard-config's own data
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return out, fmt.Errorf("layouts: open %s: %w", path, err)
		}
		err = label(f, out, missing)
		_ = f.Close()
		if err != nil {
			return out, fmt.Errorf("layouts: read %s: %w", path, err)
		}
		if len(missing) == 0 {
			return distinct(out), nil
		}
	}
	return distinct(out), fmt.Errorf("layouts: no %s under %v names %d of %q", rulesList, includePaths, len(missing), names)
}

func distinct(all []Layout) []Layout {
	seen := make(map[string]int, len(all))
	for i, l := range all {
		seen[l.Label]++
		if n := seen[l.Label]; n > 1 {
			all[i].Label += strconv.Itoa(n)
		}
	}
	return all
}

func label(f *os.File, out []Layout, missing map[string][]int) error {
	sc := bufio.NewScanner(f)
	section := ""
	for sc.Scan() && len(missing) > 0 {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "! "); ok {
			section = strings.TrimSpace(rest)
			continue
		}
		if section != "layout" && section != "variant" {
			continue
		}
		code, desc, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		desc = strings.TrimSpace(desc)
		if section == "variant" {
			if code, desc, ok = strings.Cut(desc, ": "); !ok {
				continue
			}
		}
		at, ok := missing[desc]
		if !ok {
			continue
		}
		for _, i := range at {
			out[i].Label = strings.ToUpper(code)
		}
		delete(missing, desc)
	}
	return sc.Err()
}
