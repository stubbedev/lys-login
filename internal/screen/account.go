package screen

import (
	"image"
	"log"

	"github.com/stubbedev/lys-desktop/shell/widgets"
	"github.com/stubbedev/lys-desktop/styling"
	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
)

// AvatarPx is the side of an account's round picture.
const AvatarPx = 80

// nameChars is the account name's max-width-chars.
const nameChars = 18

// AccountTile is one account as the login screen shows it: the round
// picture above the name, the picture the initials until one is set.
type AccountTile struct {
	Root   *widget.Box
	avatar *widget.Avatar
}

// NewAccountTile is the tile of the account called displayName.
func NewAccountTile(font render.Font, displayName string) *AccountTile {
	th := widget.Current()
	avatar := widget.NewAvatar(font, AvatarPx, displayName)
	avatar.AddClass("lock-avatar")
	name := widget.NewLabel(font, widgets.TextPx(styling.Subtitle, 1), displayName, 0)
	name.AddClass("lock-account-name")
	name.SetEllipsize(widget.EllipsizeEnd)
	name.SetMaxWidthChars(nameChars)
	name.SetAlignment(render.AlignCenter)
	root := widget.NewBox(widget.Column, th.Spacing, 0)
	root.AddClass("lock-account")
	root.SetHAlign(widget.AlignCenter)
	root.AppendAligned(avatar, false, widget.AlignCenter).Append(name, false)
	return &AccountTile{Root: root, avatar: avatar}
}

// Initials are what the picture shows without an image.
func (a *AccountTile) Initials() string { return a.avatar.Initials() }

// SetPicture shows img as the account's picture.
func (a *AccountTile) SetPicture(img image.Image) { a.avatar.SetImage(img) }

// AvatarPicture is the image at path decoded by load and cut to what a
// tile shows on an output of the given buffer scale, nil when there is
// none or it does not read: a photo would keep megabytes for a small
// circle otherwise.
func AvatarPicture(load func(path string) (image.Image, error), path string, scale int) image.Image {
	if path == "" {
		return nil
	}
	img, err := load(path)
	if err != nil {
		log.Printf("avatar %s unreadable, showing the initials: %v", path, err)
		return nil
	}
	return SquareThumbnail(img, AvatarPx*scale)
}

// MaxScale is the largest buffer scale of outputs, 1 without any.
func MaxScale(outputs []*app.Output) int {
	scale := 1
	for _, out := range outputs {
		scale = max(scale, out.Scale)
	}
	return scale
}
