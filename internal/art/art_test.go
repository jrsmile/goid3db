package art

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func synchsafe(n int) []byte {
	return []byte{byte((n >> 21) & 0x7f), byte((n >> 14) & 0x7f), byte((n >> 7) & 0x7f), byte(n & 0x7f)}
}

func frame(id string, data []byte) []byte {
	b := []byte(id)
	b = append(b, synchsafe(len(data))...)
	b = append(b, 0, 0)
	return append(b, data...)
}

func id3(frames ...[]byte) []byte {
	var body []byte
	for _, f := range frames {
		body = append(body, f...)
	}
	hdr := append([]byte("ID3"), 0x04, 0x00, 0x00)
	hdr = append(hdr, synchsafe(len(body))...)
	return append(hdr, body...)
}

func apic(picData []byte) []byte {
	data := []byte{0x03} // UTF-8
	data = append(data, []byte("image/png")...)
	data = append(data, 0x00) // MIME terminator
	data = append(data, 0x03) // picture type: cover (front)
	data = append(data, 0x00) // empty description terminator
	data = append(data, picData...)
	return frame("APIC", data)
}

func textFrame(id, s string) []byte {
	return frame(id, append([]byte{0x03}, []byte(s)...))
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 7), uint8(y * 11), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractSuccess(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "art.mp3", id3(apic(makePNG(t, 8, 8))))
	img, ok := Extract(p)
	if !ok || img == nil {
		t.Fatalf("expected embedded art to be extracted, ok=%v", ok)
	}
	if img.Bounds().Dx() != 8 {
		t.Errorf("unexpected image width: %d", img.Bounds().Dx())
	}
}

func TestExtractNoPicture(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "notitlepic.mp3", id3(textFrame("TIT2", "Just a title")))
	if _, ok := Extract(p); ok {
		t.Error("expected no picture for a tag without APIC")
	}
}

func TestExtractUndecodablePicture(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "bad.mp3", id3(apic([]byte("this is not an image"))))
	if _, ok := Extract(p); ok {
		t.Error("expected decode failure for non-image picture data")
	}
}

func TestExtractNoTags(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "plain.txt", []byte("hello, not audio"))
	if _, ok := Extract(p); ok {
		t.Error("expected no tags for a plain file")
	}
}

func TestExtractMissingFile(t *testing.T) {
	if _, ok := Extract(filepath.Join(t.TempDir(), "nope.mp3")); ok {
		t.Error("expected failure for missing file")
	}
}

func TestRenderGuards(t *testing.T) {
	if Render(nil, 10, 10) != nil {
		t.Error("nil image should render nil")
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	if Render(img, 0, 10) != nil {
		t.Error("zero cols should render nil")
	}
	if Render(img, 10, 0) != nil {
		t.Error("zero rows should render nil")
	}
	if Render(image.NewRGBA(image.Rect(0, 0, 0, 0)), 10, 10) != nil {
		t.Error("empty image should render nil")
	}
}

func TestRenderShapes(t *testing.T) {
	// Tall image: fitRows exceeds the row budget and is clamped.
	if lines := Render(image.NewRGBA(image.Rect(0, 0, 10, 100)), 20, 10); len(lines) == 0 {
		t.Error("tall image should produce lines")
	}
	// Wide image: fitRows stays within budget (else path).
	if lines := Render(image.NewRGBA(image.Rect(0, 0, 100, 10)), 20, 10); len(lines) == 0 {
		t.Error("wide image should produce lines")
	}
	// Extremely tall, tiny budget: forces the fitCols<1 clamp.
	if lines := Render(image.NewRGBA(image.Rect(0, 0, 1, 1000)), 5, 5); len(lines) == 0 {
		t.Error("extreme-tall image should still produce a line")
	}
	// Extremely wide, narrow budget: forces the fitRows<1 clamp.
	if lines := Render(image.NewRGBA(image.Rect(0, 0, 1000, 1)), 1, 5); len(lines) == 0 {
		t.Error("extreme-wide image should still produce a line")
	}
}

func TestSampleClamps(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	b := img.Bounds()
	// Out-of-range cell coordinates exercise the sx/sy clamp branches.
	r, g, bl := sample(img, b, 4, 4, 1, 1, 10, 10, 1)
	_ = r
	_ = g
	_ = bl
}
