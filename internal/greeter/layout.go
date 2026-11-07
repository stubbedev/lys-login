package greeter

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func lastLayoutDir(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "last-layout")
}

func LastLayoutPath(statePath, user string) (string, bool) {
	if user == "" || user == "." || user == ".." || strings.ContainsAny(user, "/\x00") {
		return "", false
	}
	return filepath.Join(lastLayoutDir(statePath), user), true
}

func LoadLayouts(statePath string) map[string]string {
	dir := lastLayoutDir(statePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("greeter: the layouts users last typed in: %v", err)
		}
		return nil
	}
	recorded := make(map[string]string, len(entries))
	for _, e := range entries {
		if layout := LoadLast(filepath.Join(dir, e.Name())); layout != "" {
			recorded[e.Name()] = layout
		}
	}
	return recorded
}

func (s *screen) wantLayoutOf(user string) {
	s.layouts.Want(s.init.Layouts[user])
}
