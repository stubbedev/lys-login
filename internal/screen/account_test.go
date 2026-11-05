package screen

import (
	"errors"
	"image"
	"testing"

	"github.com/stubbedev/lys-ui/app"
)

func TestAvatarPictureIsCutToItsSlot(t *testing.T) {
	read := 0
	load := func(string) (image.Image, error) {
		read++
		return image.NewRGBA(image.Rect(0, 0, 1000, 800)), nil
	}
	if AvatarPicture(load, "", 2) != nil || read != 0 {
		t.Error("an account without an avatar reads nothing and shows the initials")
	}
	if b := AvatarPicture(load, "/face", 2).Bounds(); b.Dx() != 2*AvatarPx || b.Dy() != 2*AvatarPx {
		t.Errorf("the picture keeps %v, want a %dpx square", b, 2*AvatarPx)
	}
	broken := func(string) (image.Image, error) { return nil, errors.New("truncated") }
	if AvatarPicture(broken, "/face", 1) != nil {
		t.Error("an unreadable avatar shows the initials")
	}
}

func TestMaxScale(t *testing.T) {
	if got := MaxScale(nil); got != 1 {
		t.Errorf("no outputs: scale %d, want 1", got)
	}
	if got := MaxScale([]*app.Output{{Scale: 1}, {Scale: 3}, {Scale: 2}}); got != 3 {
		t.Errorf("scale %d, want the largest, 3", got)
	}
}
