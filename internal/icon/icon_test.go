package icon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestICOStructure(t *testing.T) {
	b := ICO(false)
	var hdr [3]uint16
	binary.Read(bytes.NewReader(b), binary.LittleEndian, &hdr)
	if hdr[0] != 0 || hdr[1] != 1 || hdr[2] == 0 {
		t.Fatalf("bad header %v", hdr)
	}
	// first entry's image must decode as PNG
	size := binary.LittleEndian.Uint32(b[6+8:])
	off := binary.LittleEndian.Uint32(b[6+12:])
	img, err := png.Decode(bytes.NewReader(b[off : off+size]))
	if err != nil || img.Bounds().Dx() != 16 {
		t.Fatalf("first image: %v %v", img, err)
	}
}

func TestShapes(t *testing.T) {
	img := Image(64, true)
	if c := img.NRGBAAt(32, 3); c.B < 0xe0 || c.A != 0xff {
		t.Errorf("top edge should be blue, got %v", c)
	}
	if c := img.NRGBAAt(33, 36); c != white {
		t.Errorf("cloud centre should be white, got %v", c)
	}
	if c := img.NRGBAAt(0, 0); c.A != 0 {
		t.Errorf("rounded corner should be transparent, got %v", c)
	}
	if c := img.NRGBAAt(51, 51); c != green {
		t.Errorf("status dot should be green, got %v", c)
	}
}
