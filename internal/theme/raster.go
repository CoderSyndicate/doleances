package theme

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// # Why a vector project rasterises anything at all
//
// Every image here is SVG, and that is right for a masthead: one file, any
// size, and it follows an uploaded theme. A **favicon** is not that, however
// much it ought to be.
//
// Safari was the one that said so, with evidence rather than opinion. Given a
// page declaring `<link rel="icon" type="image/svg+xml">` it fetched the SVG
// twelve times and drew the generic letter tile anyway — and in the same
// minutes asked for `/favicon.ico`, `/apple-touch-icon.png` and
// `/apple-touch-icon-precomposed.png`, all three of which answered 404. A
// browser asking for a format and being refused it is not a browser that can
// be argued with.
//
// So the SVG stays the source and the raster is derived from it, at request
// time. Derived rather than committed, because the `mark` slot is a theme
// asset an operator can replace: a PNG checked into the repository would be
// this project's own mark for ever, whatever anybody uploaded.

// PNG renders an SVG into a square PNG of the given size.
//
// The rasteriser is pure Go on purpose — everything builds with
// CGO_ENABLED=0, which is what allows the shell-less, static container images,
// and a cgo image library would leak straight into the production base image.
func PNG(svg []byte, size int) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("theme: a raster needs a positive size, got %d", size)
	}

	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg), oksvg.WarnErrorMode)
	if err != nil {
		return nil, fmt.Errorf("theme: read the svg: %w", err)
	}
	icon.SetTarget(0, 0, float64(size), float64(size))

	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	scanner := rasterx.NewScannerGV(size, size, canvas, canvas.Bounds())
	icon.Draw(rasterx.NewDasher(size, size, scanner), 1)

	var out bytes.Buffer
	if err := png.Encode(&out, canvas); err != nil {
		return nil, fmt.Errorf("theme: encode the png: %w", err)
	}
	return out.Bytes(), nil
}

// ICO wraps a PNG in the container browsers ask for at /favicon.ico.
//
// An .ico may carry PNG data rather than a bitmap — every browser still in use
// reads it, and it saves writing a BMP encoder for a file whose only job is to
// exist at a fixed address. The structure is a six-byte directory, one
// sixteen-byte entry, and the image.
func ICO(image []byte, size int) ([]byte, error) {
	if size <= 0 || size > 256 {
		return nil, fmt.Errorf("theme: an icon is 1 to 256 pixels, got %d", size)
	}

	var out bytes.Buffer
	// ICONDIR: reserved, type 1 (icon), one image.
	_ = binary.Write(&out, binary.LittleEndian, uint16(0))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))

	// ICONDIRENTRY. A dimension of 256 is written as 0, which is the format
	// saying it does not fit in a byte.
	dimension := byte(size)
	if size == 256 {
		dimension = 0
	}
	out.WriteByte(dimension)                                // width
	out.WriteByte(dimension)                                // height
	out.WriteByte(0)                                        // colours in the palette; none, it is truecolour
	out.WriteByte(0)                                        // reserved
	_ = binary.Write(&out, binary.LittleEndian, uint16(1))  // colour planes
	_ = binary.Write(&out, binary.LittleEndian, uint16(32)) // bits per pixel
	_ = binary.Write(&out, binary.LittleEndian, uint32(len(image)))
	_ = binary.Write(&out, binary.LittleEndian, uint32(6+16)) // the image follows the entry

	out.Write(image)
	return out.Bytes(), nil
}
