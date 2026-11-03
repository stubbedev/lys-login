package dm

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"

	"github.com/stubbedev/lys-lib/drm"
	"golang.org/x/sys/unix"
)

const cardGlob = "/dev/dri/card[0-9]*"

type openCard interface {
	FD() int
	DropMaster() error
	Close() error
}

type heldCards struct {
	glob   string
	open   func(path string) (openCard, error)
	fromFD func(fd int, path string) openCard
	cards  map[string]openCard
}

func newHeldCards() *heldCards {
	return &heldCards{
		glob:   cardGlob,
		open:   func(path string) (openCard, error) { return drm.Open(path) },
		fromFD: func(fd int, path string) openCard { return drm.FromFD(fd, path) },
		cards:  map[string]openCard{},
	}
}

func (h *heldCards) holdAll() error {
	paths, err := filepath.Glob(h.glob)
	if err != nil {
		return fmt.Errorf("list the DRM cards %s: %w", h.glob, err)
	}
	var errs []error
	for _, path := range paths {
		if _, held := h.cards[path]; held {
			continue
		}
		card, err := h.open(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("hold %s open across compositors: %w", path, err))
			continue
		}
		if err := card.DropMaster(); err != nil && !errors.Is(err, unix.EINVAL) {
			errs = append(errs, fmt.Errorf("give up the DRM master of %s: %w", path, err), card.Close())
			continue
		}
		h.cards[path] = card
	}
	return errors.Join(errs...)
}

func (h *heldCards) held() []handedCard {
	held := make([]handedCard, 0, len(h.cards))
	for _, path := range slices.Sorted(maps.Keys(h.cards)) {
		held = append(held, handedCard{Path: path, FD: h.cards[path].FD()})
	}
	return held
}

func (h *heldCards) adopt(cards []handedCard, track func(io.Closer)) error {
	for _, card := range cards {
		dup, err := unix.FcntlInt(uintptr(card.FD), unix.F_DUPFD_CLOEXEC, firstHandoverFD)
		if err != nil {
			return fmt.Errorf("%s: duplicate descriptor %d: %w", card.Path, card.FD, err)
		}
		c := h.fromFD(dup, card.Path)
		track(c)
		h.cards[card.Path] = c
	}
	return nil
}

func (h *heldCards) Close() error {
	var errs []error
	for _, path := range slices.Sorted(maps.Keys(h.cards)) {
		errs = append(errs, h.cards[path].Close())
		delete(h.cards, path)
	}
	return errors.Join(errs...)
}
