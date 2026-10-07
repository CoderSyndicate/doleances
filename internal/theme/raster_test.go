package theme

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestTheMarkRastersToSomethingVisible.
//
// A rasteriser that silently draws nothing produces a valid, correctly sized,
// fully transparent PNG — which is exactly what an empty tab looks like, and
// is indistinguishable from success at every layer that handles it. So this
// looks at the pixels.
func TestTheMarkRastersToSomethingVisible(t *testing.T) {
	svg, _, ok := DefaultAsset("mark")
	if !ok {
		t.Fatal("there is no built-in mark to raster")
	}

	for _, size := range []int{16, 32, 180} {
		raw, err := PNG(svg, size)
		if err != nil {
			t.Errorf("%dpx: %v", size, err)
			continue
		}

		decoded, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("%dpx: the output is not a png: %v", size, err)
			continue
		}
		if got := decoded.Bounds(); got.Dx() != size || got.Dy() != size {
			t.Errorf("%dpx: bounds are %v", size, got)
		}

		opaque, ink := countPixels(decoded)
		// The mark is a filled tile, so most of it should be painted.
		if opaque < size*size/2 {
			t.Errorf("%dpx: only %d of %d pixels were painted — the rasteriser "+
				"produced a mostly empty image, which looks exactly like an empty tab",
				size, opaque, size*size)
		}
		// And the drawing on top of it has to be there, or the favicon is a
		// plain yellow square.
		if ink < size {
			t.Errorf("%dpx: only %d dark pixels — the cahier was not drawn onto the tile",
				size, ink)
		}
	}
}

// countPixels returns how many pixels are substantially opaque, and how many
// of those are dark enough to be the ink rather than the ground.
func countPixels(img image.Image) (opaque, ink int) {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			opaque++
			if r < 0x6000 && g < 0x6000 && b < 0x6000 {
				ink++
			}
		}
	}
	return opaque, ink
}

// TestTheIconWrapsThePNGWhereBrowsersLookForIt.
//
// The structure is six bytes of directory, sixteen of entry, then the image.
// Getting the offset or the length wrong produces a file every tool still
// calls an .ico and no browser will draw.
func TestTheIconWrapsThePNGWhereBrowsersLookForIt(t *testing.T) {
	svg, _, _ := DefaultAsset("mark")
	raw, err := PNG(svg, 32)
	if err != nil {
		t.Fatalf("raster: %v", err)
	}

	icon, err := ICO(raw, 32)
	if err != nil {
		t.Fatalf("ICO: %v", err)
	}
	if len(icon) != 6+16+len(raw) {
		t.Fatalf("icon is %d bytes, want %d", len(icon), 6+16+len(raw))
	}
	if kind := binary.LittleEndian.Uint16(icon[2:4]); kind != 1 {
		t.Errorf("type is %d, want 1 (an icon)", kind)
	}
	if count := binary.LittleEndian.Uint16(icon[4:6]); count != 1 {
		t.Errorf("declares %d images, want 1", count)
	}
	if icon[6] != 32 || icon[7] != 32 {
		t.Errorf("declares %dx%d, want 32x32", icon[6], icon[7])
	}

	length := binary.LittleEndian.Uint32(icon[14:18])
	offset := binary.LittleEndian.Uint32(icon[18:22])
	if int(offset) != 6+16 || int(length) != len(raw) {
		t.Fatalf("points at offset %d length %d, want %d and %d",
			offset, length, 6+16, len(raw))
	}
	if _, err := png.Decode(bytes.NewReader(icon[offset : int(offset)+int(length)])); err != nil {
		t.Errorf("what the entry points at is not a png: %v", err)
	}
}

// TestASizeOfNothingIsRefused rather than producing an empty image somebody
// then serves.
func TestASizeOfNothingIsRefused(t *testing.T) {
	svg, _, _ := DefaultAsset("mark")
	if _, err := PNG(svg, 0); err == nil {
		t.Error("a zero-sized raster was allowed")
	}
	if _, err := ICO([]byte("x"), 0); err == nil {
		t.Error("a zero-sized icon was allowed")
	}
	if _, err := ICO([]byte("x"), 512); err == nil {
		t.Error("an icon larger than the format allows was accepted")
	}
}

// dumpRasters writes the rendered files out when DOLEANCES_RASTER_DUMP names a
// directory, so a person can look at what the rasteriser actually drew. No
// test depends on it.
func TestMain(m *testing.M) {
	code := m.Run()
	if dir := os.Getenv("DOLEANCES_RASTER_DUMP"); dir != "" {
		if svg, _, ok := DefaultAsset("mark"); ok {
			for _, size := range []int{16, 32, 180} {
				if raw, err := PNG(svg, size); err == nil {
					name := filepath.Join(dir, "mark-"+itoa(size)+".png")
					_ = os.WriteFile(name, raw, 0o600)
				}
			}
		}
	}
	os.Exit(code)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
