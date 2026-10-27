package gui

import (
	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
)

// themeChoices are the 화면 테마 options: the stored value and its label.
var themeChoices = []struct{ value, label string }{
	{"", "시스템"},
	{"light", "밝게"},
	{"dark", "어둡게"},
}

type themeWidgets struct {
	btns [3]widget.Clickable
}

// wantDark is whether the window should be dark: the theme chosen by hand,
// or the system setting when none is.
func (u *UI) wantDark(systemDark bool) bool {
	switch u.svc.Theme() {
	case "light":
		return false
	case "dark":
		return true
	}
	return systemDark
}

// themeRow picks the window theme (시스템 follows Windows); the change
// shows on the next frame.
func (u *UI) themeRow(gtx C) D {
	current := u.svc.Theme()
	for i, c := range themeChoices {
		if u.themeW.btns[i].Clicked(gtx) && c.value != current {
			u.svc.SetTheme(c.value)
			current = c.value
			u.invalidate()
		}
	}
	chips := make([]layout.FlexChild, 0, 2*len(themeChoices))
	for i, c := range themeChoices {
		if i > 0 {
			chips = append(chips, hspace(6))
		}
		chips = append(chips, layout.Rigid(func(gtx C) D {
			b := smallButton(u.th, &u.themeW.btns[i], c.label, colText)
			if c.value == current {
				b.Background, b.Color = colAccent, colOnAccent
			}
			b.Inset = layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}
			return b.Layout(gtx)
		}))
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, label(u.th, 15, "화면 테마", colText, font.Medium).Layout),
		hspace(unit.Dp(12)),
		layout.Rigid(func(gtx C) D { return layout.Flex{}.Layout(gtx, chips...) }),
	)
}

// suspendRow chooses whether drives are switched off while their server
// doesn't answer, and back on when it does.
func (u *UI) suspendRow(gtx C) D {
	if u.suspendSw.Update(gtx) {
		u.svc.SetAutoSuspend(u.suspendSw.Value)
	}
	u.suspendSw.Value = u.svc.AutoSuspend()
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(label(u.th, 15, "연결이 끊기면 드라이브 잠시 끄기", colText, font.Medium).Layout),
				vspace(3),
				layout.Rigid(label(u.th, 12, "탐색기가 멈추지 않게 껐다가, 돌아오면 다시 켜고 알려 줘요", colSub, font.Normal).Layout),
			)
		}),
		hspace(12),
		layout.Rigid(toggle(u.th, &u.suspendSw, "드라이브 잠시 끄기").Layout),
	)
}
