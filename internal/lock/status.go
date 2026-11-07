package lock

import (
	"math"
	"net/url"
	"strconv"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-shell/i18n"
	"github.com/stubbedev/lys-shell/service/mpris"
	"github.com/stubbedev/lys-shell/service/upower"
	"github.com/stubbedev/lys-shell/shell/widgets"
	"github.com/stubbedev/lys-shell/styling"
	"github.com/stubbedev/lys-ui/widget"
	"github.com/stubbedev/lys-ui/widget/css"
)

const (
	mediaArtPx     = 48
	mediaControlPx = 20
	batteryIconPx  = 16
	playIcon       = "ld-play-symbolic"
	pauseIcon      = "ld-pause-symbolic"
	nextIcon       = "ld-skip-forward-symbolic"
)

type nowPlaying struct {
	bus     string
	title   string
	artist  string
	artPath string
	playing bool
	canNext bool
}

func nowPlayingOf(p mpris.Player, ok bool) (nowPlaying, bool) {
	if !ok || p.Title == "" {
		return nowPlaying{}, false
	}
	np := nowPlaying{bus: p.BusName, title: p.Title, artist: p.Artist, playing: p.State == mpris.StatePlaying, canNext: p.CanGoNext}
	if u, err := url.Parse(p.ArtURL); err == nil && u.Scheme == "file" {
		np.artPath = u.Path
	}
	return np, true
}

type batteryLevel struct {
	percent  int
	charging bool
}

func batteryLevelOf(d upower.Device) (batteryLevel, bool) {
	if !d.IsPresent {
		return batteryLevel{}, false
	}
	charging := d.State == upower.StateCharging || d.State == upower.StatePendingCharge
	return batteryLevel{percent: int(math.Round(d.Percentage)), charging: charging}, true
}

type mediaRow struct {
	root          *widget.Box
	art           *widget.Image
	artPath       string
	title, artist *widget.Label
	playIcon      *widget.Icon
	play, next    *widget.Button
}

func newMediaRow(font render.Font, onPlayPause, onNext func()) *mediaRow {
	th := widget.Current()
	px := widgets.TextPx(styling.Body, 1)
	m := &mediaRow{
		art:      widget.NewFileImage(""),
		title:    widget.NewLabel(font, px, "", 0),
		artist:   widget.NewLabel(font, px, "", 0),
		playIcon: widget.NewThemeIcon(playIcon, mediaControlPx),
	}
	m.art.AddClass("lock-media-art")
	m.art.SetScale(widget.ImageCover)
	m.art.SetSizeRequest(mediaArtPx, mediaArtPx)
	m.art.SetMaximumSize(mediaArtPx, mediaArtPx)
	m.title.AddClass("lock-media-title", css.Heading)
	m.artist.AddClass("lock-media-artist", css.DimLabel)
	m.play = controlButton(m.playIcon, onPlayPause)
	m.next = controlButton(widget.NewThemeIcon(nextIcon, mediaControlPx), onNext)
	m.next.SetAccessibleName(i18n.T("lock-media-next"))
	track := widget.NewBox(widget.Column, 0, 0)
	track.Append(m.title, false).Append(m.artist, false)
	m.root = widget.NewBox(widget.Row, th.Spacing, 0)
	m.root.AddClass("lock-media")
	m.root.AppendAligned(m.art, false, widget.AlignCenter)
	m.root.AppendAligned(track, false, widget.AlignCenter)
	m.root.AppendAligned(m.play, false, widget.AlignCenter)
	m.root.AppendAligned(m.next, false, widget.AlignCenter)
	m.root.SetVisible(false)
	return m
}

func controlButton(icon *widget.Icon, onClick func()) *widget.Button {
	b := widget.NewButton(icon, 0, 0)
	b.AddClass("lock-media-control", css.Ghost, css.Icon)
	b.SetCursorName("pointer")
	b.OnClick = onClick
	return b
}

func (m *mediaRow) set(np nowPlaying, ok bool) {
	m.root.SetVisible(ok)
	if !ok {
		return
	}
	m.title.SetText(np.title)
	m.artist.SetText(np.artist)
	m.artist.SetVisible(np.artist != "")
	if np.artPath != m.artPath {
		m.artPath = np.artPath
		m.art.SetFile(np.artPath)
	}
	m.art.SetVisible(np.artPath != "")
	icon, name := playIcon, i18n.T("lock-media-play")
	if np.playing {
		icon, name = pauseIcon, i18n.T("lock-media-pause")
	}
	m.playIcon.SetThemeName(icon)
	m.play.SetAccessibleName(name)
	m.next.SetVisible(np.canNext)
}

type batteryChip struct {
	root     *widget.Box
	charging *widget.Icon
	label    *widget.Label
}

func newBatteryChip(font render.Font, chargingIcon string) *batteryChip {
	th := widget.Current()
	b := &batteryChip{
		charging: widget.NewThemeIcon(chargingIcon, batteryIconPx),
		label:    widget.NewLabel(font, widgets.TextPx(styling.Body, 1), "", 0),
	}
	b.charging.SetAccessibleName(i18n.T("lock-battery-charging"))
	b.label.AddClass(css.Numeric)
	b.root = widget.NewBox(widget.Row, th.Spacing/2, 0)
	b.root.AddClass("lock-battery")
	b.root.AppendAligned(b.charging, false, widget.AlignCenter)
	b.root.AppendAligned(b.label, false, widget.AlignCenter)
	b.root.SetVisible(false)
	return b
}

func (b *batteryChip) set(l batteryLevel, ok bool) {
	b.root.SetVisible(ok)
	if !ok {
		return
	}
	b.label.SetText(i18n.T("lock-battery", i18n.Str("percent", strconv.Itoa(l.percent))))
	b.charging.SetVisible(l.charging)
}

func switchUserButton(font render.Font, onClick func()) *widget.Button {
	label := widget.NewLabel(font, widgets.TextPx(styling.Body, 1), i18n.T("lock-switch-user"), 0)
	b := widget.NewButton(label, 0, 0)
	b.AddClass("lock-switch-user", css.Pill, css.Ghost)
	b.SetCursorName("pointer")
	b.OnClick = onClick
	return b
}
