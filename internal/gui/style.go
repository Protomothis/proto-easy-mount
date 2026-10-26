package gui

import (
	"fmt"
	"image"
	"image/color"
	"runtime"

	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

type (
	C = layout.Context
	D = layout.Dimensions
)

// Palette: calm surfaces with one strong accent. usePalette switches the
// colors between the light and dark set; only the window's goroutine may
// call it.
var (
	colBg, colCard, colText, colSub, colFaint, colLine  color.NRGBA
	colAccent, colAccentDim, colDanger, colOK, colGhost color.NRGBA
	colOnAccent                                         color.NRGBA // text on accent fills
)

type palette [12]uint32

var (
	lightPalette = palette{0xf2f4f6, 0xffffff, 0x191f28, 0x6b7684, 0xb0b8c1, 0xe5e8eb,
		0x3182f6, 0xe8f3ff, 0xf04452, 0x00a661, 0xf2f4f6, 0xffffff}
	darkPalette = palette{0x17171c, 0x222228, 0xe8e8ea, 0x9e9ea4, 0x5c5c66, 0x2e2e36,
		0x4593fc, 0x1c2a40, 0xfa616d, 0x1cc47b, 0x2e2e36, 0xffffff}
)

func init() { usePalette(false) }

func usePalette(dark bool) {
	p := lightPalette
	if dark {
		p = darkPalette
	}
	for i, c := range []*color.NRGBA{&colBg, &colCard, &colText, &colSub, &colFaint, &colLine,
		&colAccent, &colAccentDim, &colDanger, &colOK, &colGhost, &colOnAccent} {
		*c = rgb(p[i])
	}
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

func newTheme() *material.Theme {
	th := material.NewTheme()
	// System fonts are loaded too, so Hangul falls back to the OS font.
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	th.Face = systemFace()
	setThemeColors(th)
	th.TextSize = 15
	return th
}

// setThemeColors copies the current palette into th.
func setThemeColors(th *material.Theme) {
	th.Palette = material.Palette{Bg: colBg, Fg: colText, ContrastBg: colAccent, ContrastFg: colOnAccent}
}

func systemFace() font.Typeface {
	switch runtime.GOOS {
	case "windows":
		return "Malgun Gothic"
	case "darwin":
		return "Apple SD Gothic Neo"
	}
	return "Noto Sans CJK KR"
}

func label(th *material.Theme, size unit.Sp, txt string, col color.NRGBA, weight font.Weight) material.LabelStyle {
	l := material.Label(th, size, txt)
	l.Color = col
	l.Font.Weight = weight
	return l
}

// surface fills the available width with a rounded background behind w.
func surface(gtx C, bg color.NRGBA, radius unit.Dp, w layout.Widget) D {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Stack{}.Layout(gtx,
		layout.Expanded(func(gtx C) D {
			r := image.Rectangle{Max: gtx.Constraints.Min}
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(radius)).Op(gtx.Ops))
			return D{Size: gtx.Constraints.Min}
		}),
		layout.Stacked(w),
	)
}

// card draws a white rounded panel that fills the available width.
func card(gtx C, w layout.Widget) D {
	return surface(gtx, colCard, 16, func(gtx C) D {
		return layout.UniformInset(unit.Dp(20)).Layout(gtx, w)
	})
}

func primaryButton(th *material.Theme, clk *widget.Clickable, txt string) material.ButtonStyle {
	b := material.Button(th, clk, txt)
	b.Background = colAccent
	b.Color = colOnAccent
	b.CornerRadius = unit.Dp(12)
	b.Inset = layout.Inset{Top: 13, Bottom: 13, Left: 16, Right: 16}
	b.Font.Weight = font.SemiBold
	b.TextSize = 15
	return b
}

// smallButton is a quiet grey pill for secondary actions.
func smallButton(th *material.Theme, clk *widget.Clickable, txt string, fg color.NRGBA) material.ButtonStyle {
	b := material.Button(th, clk, txt)
	b.Background = colGhost
	b.Color = fg
	b.CornerRadius = unit.Dp(8)
	b.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}
	b.TextSize = 13
	b.Font.Weight = font.Medium
	return b
}

func field(th *material.Theme, ed *widget.Editor, hint string) layout.Widget {
	return func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		border := widget.Border{Color: colLine, CornerRadius: unit.Dp(12), Width: unit.Dp(1)}
		return border.Layout(gtx, func(gtx C) D {
			return layout.Inset{Top: 13, Bottom: 13, Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
				e := material.Editor(th, ed, hint)
				e.HintColor = colFaint
				e.TextSize = 15
				return e.Layout(gtx)
			})
		})
	}
}

func toggle(th *material.Theme, b *widget.Bool, desc string) material.SwitchStyle {
	s := material.Switch(th, b, desc)
	s.Color.Enabled = colAccent
	s.Color.Disabled = colOnAccent
	s.Color.Track = colFaint
	return s
}

// usageBar draws a thin rounded meter filled to frac (0…1).
func usageBar(gtx C, frac float64, fill color.NRGBA) D {
	w, h := gtx.Constraints.Max.X, gtx.Dp(6)
	r := h / 2
	paint.FillShape(gtx.Ops, colLine, clip.UniformRRect(image.Rect(0, 0, w, h), r).Op(gtx.Ops))
	if frac > 0 {
		fw := max(int(float64(w)*min(frac, 1)), h) // never thinner than its own rounding
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(0, 0, fw, h), r).Op(gtx.Ops))
	}
	return D{Size: image.Pt(w, h)}
}

// humanBytes formats a size the way Explorer does (1024-based).
func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	switch {
	case i == 0:
		return fmt.Sprintf("%d B", n)
	case v >= 100:
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func vspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Height: dp}.Layout)
}

func hspace(dp unit.Dp) layout.FlexChild {
	return layout.Rigid(layout.Spacer{Width: dp}.Layout)
}
