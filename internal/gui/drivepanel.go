package gui

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/protomothis/proto-easy-mount/internal/core"
)

func (dw *driveWidgets) modeBtn(m core.Mode) *widget.Clickable {
	if dw.modes == nil {
		dw.modes = map[core.Mode]*widget.Clickable{}
	}
	c := dw.modes[m]
	if c == nil {
		c = new(widget.Clickable)
		dw.modes[m] = c
	}
	return c
}

// modeInfo is what each upload mode is called and promises.
var modeInfo = []struct {
	mode              core.Mode
	title, desc, note string
}{
	{core.Stable, "안정형", "복사는 바로 끝나고, 업로드는 뒤에서 이어져요.", ""},
	{core.Instant, "즉시 반영형", "복사가 끝나면 서버에도 올라가 있어요.", "64MB 넘는 파일은 업로드한 시각으로 저장돼요."},
}

// drivePanel holds a drive's settings: its drive letter and, where the
// server allows it, how writes are uploaded.
func (u *UI) drivePanel(gtx C, dw *driveWidgets, d core.DriveView, letters bool) D {
	var parts []layout.FlexChild
	if letters {
		parts = append(parts, layout.Rigid(func(gtx C) D { return u.letterPicker(gtx, dw, d.Preferred) }))
	}
	if d.InstantOK {
		if letters {
			parts = append(parts, vspace(8))
		}
		parts = append(parts,
			layout.Rigid(label(u.th, 12, "업로드 방식 · 다음 연결부터 적용", colSub, font.Normal).Layout),
			vspace(8),
		)
		for i, m := range modeInfo {
			if i > 0 {
				parts = append(parts, vspace(6))
			}
			parts = append(parts, layout.Rigid(func(gtx C) D { return u.modeCard(gtx, dw.modeBtn(m.mode), m.title, m.desc, m.note, d.Mode == m.mode) }))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, parts...)
}

// modeCard is a selectable option: a title, one line of explanation and
// an optional caveat.
func (u *UI) modeCard(gtx C, clk *widget.Clickable, title, desc, note string, selected bool) D {
	bg, fg := colGhost, colText
	if selected {
		bg, fg = colAccentDim, colAccent
	}
	return clk.Layout(gtx, func(gtx C) D {
		return surface(gtx, bg, 10, func(gtx C) D {
			return layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
				mark := "○"
				if selected {
					mark = "●"
				}
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(label(u.th, 13, mark, fg, font.Normal).Layout),
					hspace(8),
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 13, title, fg, font.SemiBold).Layout),
							vspace(2),
							layout.Rigid(label(u.th, 12, desc, colSub, font.Normal).Layout),
							layout.Rigid(func(gtx C) D {
								if note == "" {
									return D{}
								}
								return layout.Inset{Top: 2}.Layout(gtx, label(u.th, 11, note, colFaint, font.Normal).Layout)
							}),
						)
					}),
				)
			})
		})
	})
}
