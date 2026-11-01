package dm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

type fakeCard struct {
	path    string
	dropErr error
	dropped bool
	closed  bool
}

func (c *fakeCard) DropMaster() error { c.dropped = true; return c.dropErr }
func (c *fakeCard) Close() error      { c.closed = true; return nil }

func TestHeldCardsKeepEveryCardOpenWithoutMaster(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	touch("card0")
	touch("renderD128")
	opened := map[string]*fakeCard{}
	var order []string
	h := &heldCards{glob: filepath.Join(dir, "card[0-9]*"), cards: map[string]openCard{}, open: func(path string) (openCard, error) {
		c := &fakeCard{path: path, dropErr: unix.EINVAL}
		opened[path] = c
		order = append(order, filepath.Base(path))
		return c, nil
	}}
	if err := h.holdAll(); err != nil {
		t.Fatal(err)
	}
	touch("card1")
	if err := h.holdAll(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"card0", "card1"}) {
		t.Fatalf("opened %v, want each card once and no render node", order)
	}
	for path, c := range opened {
		if !c.dropped || c.closed {
			t.Errorf("%s: dropped master %t, closed %t; want it held open without master", path, c.dropped, c.closed)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	for path, c := range opened {
		if !c.closed {
			t.Errorf("%s is still open after Close", path)
		}
	}
}

func TestHeldCardsLetGoOfACardWhoseMasterTheyCannotDrop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "card0"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &fakeCard{dropErr: unix.EPERM}
	h := &heldCards{glob: filepath.Join(dir, "card[0-9]*"), cards: map[string]openCard{}, open: func(string) (openCard, error) { return c, nil }}
	if err := h.holdAll(); !errors.Is(err, unix.EPERM) {
		t.Fatalf("holdAll = %v, want the failed drop reported", err)
	}
	if !c.closed || len(h.cards) != 0 {
		t.Error("a card kept as DRM master would block the next compositor")
	}
}
