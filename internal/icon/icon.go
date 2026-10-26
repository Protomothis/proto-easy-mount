// Package icon draws the app icon at runtime, so no binary assets are needed:
// a blue rounded square with a white cloud, plus a green dot while drives
// are connected.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

var (
	blue  = color.NRGBA{0x31, 0x82, 0xf6, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
	green = color.NRGBA{0x00, 0xc4, 0x71, 0xff}
)

// Image renders the icon at size×size pixels.
func Image(size int, active bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	px := 1 / float64(size) // one pixel in unit coordinates, for anti-aliasing
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			u, v := (float64(x)+0.5)*px, (float64(y)+0.5)*px
			c := color.NRGBA{}
			c = over(c, blue, cover(roundBox(u, v, 0.5, 0.5, 0.5, 0.5, 0.22), px))
			c = over(c, white, cover(cloud(u, v), px))
			if active {
				dot := circle(u, v, 0.8, 0.8, 0.17)
				c = over(c, white, cover(dot, px))
				c = over(c, green, cover(dot+0.05, px))
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

// PNG encodes the icon as PNG.
func PNG(size int, active bool) []byte {
	var buf bytes.Buffer
	png.Encode(&buf, Image(size, active))
	return buf.Bytes()
}

// ICO packs PNG renditions into a Windows .ico file.
func ICO(active bool) []byte {
	sizes := []int{16, 24, 32, 48, 64, 256}
	images := make([][]byte, len(sizes))
	for i, s := range sizes {
		images[i] = PNG(s, active)
	}
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := byte(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		binary.Write(&buf, binary.LittleEndian, struct {
			W, H, Colors, Reserved byte
			Planes, BitCount       uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(images[i])), uint32(offset)})
		offset += len(images[i])
	}
	for _, b := range images {
		buf.Write(b)
	}
	return buf.Bytes()
}

// Signed distance functions in unit coordinates (negative inside).

func circle(x, y, cx, cy, r float64) float64 { return math.Hypot(x-cx, y-cy) - r }

func roundBox(x, y, cx, cy, hw, hh, r float64) float64 {
	qx := math.Abs(x-cx) - hw + r
	qy := math.Abs(y-cy) - hh + r
	outside := math.Hypot(math.Max(qx, 0), math.Max(qy, 0))
	return outside + math.Min(math.Max(qx, qy), 0) - r
}

func cloud(x, y float64) float64 {
	d := circle(x, y, 0.38, 0.55, 0.13)
	d = math.Min(d, circle(x, y, 0.53, 0.46, 0.17))
	d = math.Min(d, circle(x, y, 0.67, 0.57, 0.11))
	return math.Min(d, roundBox(x, y, 0.52, 0.6, 0.255, 0.08, 0.08))
}

// cover turns a distance into pixel coverage with a one-pixel soft edge.
func cover(d, px float64) float64 {
	return math.Max(0, math.Min(1, 0.5-d/px))
}

// over composites src with coverage a on top of dst (straight alpha).
func over(dst, src color.NRGBA, a float64) color.NRGBA {
	if a <= 0 {
		return dst
	}
	sa := a * float64(src.A) / 255
	da := float64(dst.A) / 255
	oa := sa + da*(1-sa)
	if oa == 0 {
		return color.NRGBA{}
	}
	mix := func(s, d uint8) uint8 {
		return uint8(math.Round((float64(s)*sa + float64(d)*da*(1-sa)) / oa))
	}
	return color.NRGBA{mix(src.R, dst.R), mix(src.G, dst.G), mix(src.B, dst.B), uint8(math.Round(oa * 255))}
}
