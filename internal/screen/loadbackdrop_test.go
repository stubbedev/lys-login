package screen

import (
	"image"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stubbedev/lys-shell/imagedecode"
	"github.com/stubbedev/lys-shell/shell/treetest"
	"github.com/stubbedev/memory-go"
)

// progressivePhoto is the large progressive JPEG lys-shell's jpeg
// decoder is tested with, read from the lys-shell module this one
// builds against.
func progressivePhoto(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/stubbedev/lys-shell").Output()
	if err != nil {
		t.Fatalf("locate the lys-shell module: %v", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "third_party", "jpeg", "testdata", "flat-1024.progressive.444.jpeg")
}

func TestLoadBackdropFreesTheDecodedJPEG(t *testing.T) {
	photo := progressivePhoto(t)
	onHeap := treetest.HeapBytes(func() {
		if _, err := imagedecode.Open(photo); err != nil {
			t.Fatal(err)
		}
	})
	if onHeap < 15<<20 {
		t.Fatalf("decoding onto the heap took only %d bytes: the photo no longer measures anything", onHeap)
	}
	var mem memory.Shared
	var bg *Backdrop
	heap := treetest.HeapBytes(func() {
		var err error
		if bg, err = LoadBackdrop(&mem, imagedecode.OpenOffHeap, photo, 4, []image.Point{{256, 144}}); err != nil {
			t.Fatal(err)
		}
	})
	if heap > 1<<20 {
		t.Errorf("loading the backdrop took %d bytes of Go heap, want at most 1 MiB (the decode alone takes %d on the heap)", heap, onHeap)
	}
	if got := bg.Image.Bounds().Size(); got != image.Pt(256, 256) {
		t.Errorf("backdrop %v, want the 1024x1024 photo covering 256x144", got)
	}
	if st := mem.Stats(); st.Live != 1 || st.Bytes >= 2*4*256*256 {
		t.Errorf("after loading: %d allocations of %d bytes, want the backdrop alone", st.Live, st.Bytes)
	}
	if err := bg.Free(); err != nil {
		t.Fatal(err)
	}
	if st := mem.Stats(); st.Live != 0 {
		t.Errorf("after Free: %d allocations", st.Live)
	}
}

func TestLoadBackdropOfAnUnreadableFileHoldsNothing(t *testing.T) {
	var mem memory.Shared
	for name, path := range map[string]string{
		"missing":    filepath.Join(t.TempDir(), "missing.jpg"),
		"not a jpeg": "loadbackdrop_test.go",
	} {
		bg, err := LoadBackdrop(&mem, imagedecode.OpenOffHeap, path, 4, []image.Point{{256, 144}})
		if err == nil || bg != nil {
			t.Errorf("%s: made a backdrop", name)
			continue
		}
		if !strings.Contains(err.Error(), "unreadable") {
			t.Errorf("%s: error %q does not say the background is unreadable", name, err)
		}
	}
	if st := mem.Stats(); st.Live != 0 {
		t.Errorf("%d allocations left after unreadable backgrounds", st.Live)
	}
	_, err := LoadBackdrop(&mem, func(*memory.Shared, string) (imagedecode.Decoded, error) {
		return imagedecode.Decoded{Image: image.NewRGBA(image.Rectangle{})}, nil
	}, "/empty.png", 4, nil)
	if err == nil || !strings.Contains(err.Error(), "unusable") {
		t.Errorf("an empty image: %v, want it unusable", err)
	}
	if st := mem.Stats(); st.Live != 0 {
		t.Errorf("%d allocations left after an unusable background", st.Live)
	}
}
