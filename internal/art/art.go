// Package art extracts embedded album artwork and renders it as ANSI
// truecolor half-blocks for display in the terminal. Each character cell shows
// two vertical pixels (▀ with a foreground/background color), doubling the
// effective vertical resolution.
package art

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"strings"

	// Register decoders for the common embedded-art formats.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/dhowden/tag"
)

// Extract returns the decoded album-art image for the file at path, or
// ok=false if the file has no embedded picture or cannot be decoded.
func Extract(path string) (img image.Image, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil, false
	}
	pic := m.Picture()
	if pic == nil || len(pic.Data) == 0 {
		return nil, false
	}
	img, _, err = image.Decode(bytes.NewReader(pic.Data))
	if err != nil {
		return nil, false
	}
	return img, true
}

// Render draws img into a block of at most cols×rows terminal cells using
// ANSI truecolor half-blocks, preserving aspect ratio. Returns one string per
// line (no trailing newline) so the caller can place it in a layout.
//
// Terminal cells are roughly twice as tall as wide; combined with the
// half-block trick (2 pixels per cell vertically) the sampling grid is
// cols×(rows*2) source pixels.
func Render(img image.Image, cols, rows int) []string {
	if img == nil || cols < 1 || rows < 1 {
		return nil
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return nil
	}

	// Fit the image into cols×rows cells preserving aspect ratio. A cell is
	// ~2x taller than wide, and each cell holds 2 vertical pixels, so the
	// vertical pixel count per cell row is effectively balanced by halving.
	cellAspect := 2.0 // height/width of a character cell
	imgAspect := float64(sh) / float64(sw)
	// Target cells: scale so neither dimension exceeds the budget.
	fitCols := cols
	fitRows := int(float64(fitCols) * imgAspect / cellAspect)
	if fitRows > rows {
		fitRows = rows
		fitCols = int(float64(fitRows) * cellAspect / imgAspect)
	}
	if fitCols < 1 {
		fitCols = 1
	}
	if fitRows < 1 {
		fitRows = 1
	}

	lines := make([]string, 0, fitRows)
	var sb strings.Builder
	for cy := 0; cy < fitRows; cy++ {
		sb.Reset()
		for cx := 0; cx < fitCols; cx++ {
			// Top and bottom source pixels for this cell.
			tr, tg, tb := sample(img, b, sw, sh, fitCols, fitRows, cx, cy, 0)
			br, bg, bb := sample(img, b, sw, sh, fitCols, fitRows, cx, cy, 1)
			// Foreground = top pixel, background = bottom pixel, glyph ▀.
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", tr, tg, tb, br, bg, bb)
		}
		sb.WriteString("\x1b[0m")
		lines = append(lines, sb.String())
	}
	return lines
}

// sample returns the 8-bit RGB of the source pixel mapped to cell (cx,cy) and
// half (0=top, 1=bottom) using nearest-neighbor sampling.
func sample(img image.Image, b image.Rectangle, sw, sh, cols, rows, cx, cy, half int) (uint8, uint8, uint8) {
	// Vertical resolution is rows*2 (two pixels per cell).
	py := cy*2 + half
	sx := b.Min.X + cx*sw/cols
	sy := b.Min.Y + py*sh/(rows*2)
	if sx >= b.Max.X {
		sx = b.Max.X - 1
	}
	if sy >= b.Max.Y {
		sy = b.Max.Y - 1
	}
	r, g, bl, _ := img.At(sx, sy).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)
}
