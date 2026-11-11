package greeter

import (
	"image"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stubbedev/lys-lib/render"
	"github.com/stubbedev/lys-ui/app"
	"github.com/stubbedev/lys-ui/widget"
)

func installTestIcons(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := writeIcons(dir); err != nil {
		t.Fatal(err)
	}
	paths := widget.IconSearchPaths()
	widget.SetIconSearchPaths(append([]string{dir}, paths...))
	widget.RefreshIcons()
	t.Cleanup(func() {
		widget.SetIconSearchPaths(paths)
		widget.RefreshIcons()
	})
}

func firstFrameAt(root widget.Widget, scale int) {
	widget.SetDeviceScale(root, scale, 1)
	logical := image.Pt(640, 400)
	device := logical.Mul(scale)
	widget.SizeWithin(root, widget.Size{W: logical.X, H: logical.Y})
	root.Arrange(render.Rect{W: logical.X, H: logical.Y})
	data := make([]byte, render.Stride(device.X)*device.Y)
	root.Paint(render.NewScaled(data, render.Stride(device.X), device.X, device.Y, scale, 1))
	widget.WaitAssetLoads()
}

func TestTheGreetersFirstFrameAsksForNoIconItsPreloadDecoded(t *testing.T) {
	installTestIcons(t)
	asked := 0
	t.Cleanup(widget.SetInvoker(func(func()) { asked++ }))
	r := newRig(t, func(i *Init) { i.HandOver = true })
	widget.SetStyleRoot(r.s.root)
	r.s.start()

	const preloaded, other = 3, 5
	icons := iconPreload{scales: []int{preloaded}}
	icons.start(preloadButtonIcons)
	icons.start(r.s.prompt.PreloadIcons)
	icons.wait()

	firstFrameAt(r.s.root, preloaded)
	if asked != 0 || widget.TreeAssetsOwed(r.s.root) {
		t.Errorf("the first frame at the preloaded scale asked for %d icon(s) off the loop: it would be presented without them, or held for them", asked)
	}
	firstFrameAt(r.s.root, other)
	if asked == 0 {
		t.Error("a frame at a scale nothing was preloaded for asked for no icon: this test would not see a missing preload")
	}
}

func TestAnIconThatCannotBePreloadedIsNamed(t *testing.T) {
	paths := widget.IconSearchPaths()
	widget.SetIconSearchPaths([]string{t.TempDir()})
	widget.RefreshIcons()
	t.Cleanup(func() {
		widget.SetIconSearchPaths(paths)
		widget.RefreshIcons()
	})
	err := preloadButtonIcons(7, 1)
	if err == nil {
		t.Fatal("preloading icons no search path holds reported no error")
	}
	for _, name := range buttonIcons {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}

func TestThePreloadIsDoneWhenWaitReturns(t *testing.T) {
	icons := iconPreload{scales: []int{1, 2}}
	var decoded atomic.Int64
	release := make(chan struct{})
	for range 3 {
		icons.start(func(num, denom int) error {
			<-release
			decoded.Add(int64(num))
			return nil
		})
	}
	close(release)
	icons.wait()
	if got := decoded.Load(); got != 9 {
		t.Errorf("wait returned with decodes summing to %d, want every scale of every start: 9", got)
	}
}

func TestOutputScalesAreDistinctAndOrdered(t *testing.T) {
	outputs := []*app.Output{{Scale: 2}, {Scale: 1}, {Scale: 2}}
	if got, want := outputScales(outputs), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("scales %v, want %v", got, want)
	}
	if got := outputScales(nil); len(got) != 0 {
		t.Errorf("no outputs gave scales %v", got)
	}
}
