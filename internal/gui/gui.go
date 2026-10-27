// Package gui is the desktop window: add accounts, switch drives on and off,
// and change settings. The window can be closed at any time; the app keeps
// running in the tray.
package gui

import (
	"context"
	"fmt"
	"image"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/protomothis/proto-easy-mount/internal/autostart"
	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/icon"
	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/provider"
)

// UI owns at most one window at a time.
type UI struct {
	svc     *core.Service
	version string
	quit    func() // ends the whole app, not just the window
	// activity reports transfers; the service by default, swappable in tests
	activity func() core.Activity
	// LogPath is shown via the "로그 폴더" button; set before Show.
	LogPath string
	// Install runs a downloaded update: quit the app, then start the installer.
	Install func(installer string)
	// OnUpdate runs when the update state changes (the tray shows it).
	OnUpdate func()

	upd    updater
	aboutW aboutWidgets

	mu  sync.Mutex
	win *app.Window
	// accounts with a logout in flight
	busy map[string]bool

	// Widget state below is only touched on the window goroutine.
	th        *material.Theme
	logo      paint.ImageOp
	list      widget.List
	add       *addFlow
	addBtn    widget.Clickable
	autoStart widget.Bool
	suspendSw widget.Bool // 네트워크가 끊기면 드라이브 잠시 끄기
	autoErr   string
	showAdd   bool
	quitBtn   widget.Clickable
	logBtn    widget.Clickable
	dark      bool // palette in use
	themeW    themeWidgets
	quitArmed time.Time // first click while drives are mounted only arms the button
	accts     map[string]*acctWidgets
	drives    map[string]*driveWidgets
}

type acctWidgets struct {
	refresh, logout widget.Clickable
	signIn          widget.Clickable
	confirmUntil    time.Time
	signingIn       atomic.Bool // a sign-in again is waiting for the browser
}

type driveWidgets struct {
	sw     widget.Bool
	open   widget.Clickable
	letter widget.Clickable
	// drive letter picker
	picking bool
	free    []string
	chips   map[string]*widget.Clickable
	modes   map[core.Mode]*widget.Clickable
	note    string
}

func (dw *driveWidgets) chip(v string) *widget.Clickable {
	if dw.chips == nil {
		dw.chips = map[string]*widget.Clickable{}
	}
	c := dw.chips[v]
	if c == nil {
		c = new(widget.Clickable)
		dw.chips[v] = c
	}
	return c
}

// New creates the UI; nothing is shown until Show. quit ends the app.
func New(svc *core.Service, version string, quit func()) *UI {
	u := &UI{
		svc:     svc,
		version: version,
		quit:    quit,
		busy:    map[string]bool{},
		accts:   map[string]*acctWidgets{},
		drives:  map[string]*driveWidgets{},
	}
	u.list.Axis = layout.Vertical
	u.add = newAddFlow(u)
	u.activity = svc.Activity
	u.autoStart.Value = autostart.Enabled()
	svc.Subscribe(u.invalidate)
	return u
}

// Show opens the window, or brings it to the front. Safe from any goroutine.
func (u *UI) Show() {
	u.mu.Lock()
	if w := u.win; w != nil {
		u.mu.Unlock()
		w.Perform(system.ActionRaise)
		return
	}
	w := new(app.Window)
	u.win = w
	u.mu.Unlock()

	w.Option(
		app.Title("Proto Easy Mount"),
		app.Size(unit.Dp(440), unit.Dp(640)),
		app.MinSize(unit.Dp(360), unit.Dp(480)),
	)
	go u.loop(w)
}

func (u *UI) invalidate() {
	u.mu.Lock()
	w := u.win
	u.mu.Unlock()
	if w != nil {
		w.Invalidate()
	}
}

func (u *UI) loop(w *app.Window) {
	if u.th == nil {
		u.th = newTheme()
		u.logo = paint.NewImageOp(icon.Image(128, false))
	}
	w.Perform(system.ActionCenter)
	stop := make(chan struct{})
	defer close(stop)
	var sysDark atomic.Bool
	sysDark.Store(systemDark())
	go watchTheme(w, &sysDark, stop)

	var ops op.Ops
	var hwnd uintptr
	for {
		e := w.Event()
		if h, ok := windowHandle(e); ok {
			hwnd = h
			darkTitleBar(hwnd, u.dark)
		}
		switch e := e.(type) {
		case app.DestroyEvent:
			u.mu.Lock()
			u.win = nil
			u.mu.Unlock()
			return
		case app.FrameEvent:
			if d := u.wantDark(sysDark.Load()); d != u.dark {
				u.setDark(d)
				darkTitleBar(hwnd, d)
			}
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, colBg)
			u.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// setDark switches the palette. Call on the window goroutine.
func (u *UI) setDark(dark bool) {
	u.dark = dark
	usePalette(dark)
	setThemeColors(u.th)
}

// watchTheme follows the system light/dark setting while the window is
// open, redrawing when it changes.
func watchTheme(w *app.Window, dark *atomic.Bool, stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if d := systemDark(); d != dark.Load() {
				dark.Store(d)
				w.Invalidate()
			}
		}
	}
}

func (u *UI) layout(gtx C) D {
	accts := u.svc.Snapshot()
	u.add.update(gtx)

	var sections []layout.Widget
	sections = append(sections, u.header)
	if u.active() {
		sections = append(sections, u.activityBar)
	}
	for _, a := range accts {
		sections = append(sections, func(gtx C) D { return u.account(gtx, a) })
	}
	if len(accts) == 0 || u.showAdd {
		sections = append(sections, func(gtx C) D { return u.add.layout(gtx, len(accts) > 0) })
	} else {
		if u.addBtn.Clicked(gtx) {
			u.add.reset()
			u.showAdd = true
		}
		sections = append(sections, func(gtx C) D {
			b := smallButton(u.th, &u.addBtn, "+ 다른 저장소 연결", colAccent)
			b.Background = colAccentDim
			b.Inset = layout.UniformInset(12)
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return b.Layout(gtx)
		})
	}
	sections = append(sections, u.about, u.settings)

	return material.List(u.th, &u.list).Layout(gtx, len(sections), func(gtx C, i int) D {
		in := layout.Inset{Left: 16, Right: 16, Top: 6, Bottom: 6}
		if i == 0 {
			in.Top = 20
		}
		if i == len(sections)-1 {
			in.Bottom = 20
		}
		return in.Layout(gtx, sections[i])
	})
}

func (u *UI) header(gtx C) D {
	return layout.Inset{Left: 4, Right: 4, Bottom: 10}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				sz := gtx.Dp(44)
				gtx.Constraints = layout.Exact(image.Pt(sz, sz))
				return widget.Image{Src: u.logo, Fit: widget.Contain}.Layout(gtx)
			}),
			hspace(14),
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(label(u.th, 20, "Proto Easy Mount", colText, font.Bold).Layout),
					vspace(2),
					layout.Rigid(label(u.th, 13, "클라우드를 내 컴퓨터 드라이브처럼", colSub, font.Normal).Layout),
				)
			}),
		)
	})
}

// speedText describes current speeds, or "" when nothing moves.
func speedText(act core.Activity) string {
	var parts []string
	// below 1 KB/s is the tail of a smoothed average, not a transfer
	if act.Up >= 1024 {
		parts = append(parts, "↑ "+humanBytes(act.Up)+"/s")
	}
	if act.Down >= 1024 {
		parts = append(parts, "↓ "+humanBytes(act.Down)+"/s")
	}
	return strings.Join(parts, "   ")
}

// maxFiles is how many uploads the activity strip lists by name.
const maxFiles = 3

// activityBar is a full-width strip under the header while files move.
// In stable mode Explorer reports a copy as done once it reaches the local
// write cache and the upload follows in the background, so say that
// plainly; instant-mode copies end only when the server has the file.
func (u *UI) activityBar(gtx C) D {
	act := u.activity()
	speeds := speedText(act)
	title, note := "내려받는 중", ""
	if act.Uploads > 0 {
		title = fmt.Sprintf("서버로 올리는 중 · %d개 남음", act.Uploads)
		instant := 0
		for _, f := range act.Files {
			if f.Instant {
				instant++
			}
		}
		if act.Uploads > instant {
			note = "탐색기 복사는 끝났어요. 업로드가 끝날 때까지 앱을 끄지 마세요."
		} else {
			note = "탐색기 복사가 끝나면 서버에도 저장된 거예요."
		}
	}
	files := act.Files
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	// queued uploads have no transfer yet, so count them from the total
	more := max(act.Uploads, len(act.Files)) - len(files)

	return surface(gtx, colAccentDim, 12, func(gtx C) D {
		return layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 14}.Layout(gtx, func(gtx C) D {
			rows := []layout.FlexChild{
				layout.Rigid(func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, label(u.th, 13, title, colAccent, font.SemiBold).Layout),
						layout.Rigid(label(u.th, 13, speeds, colAccent, font.Medium).Layout),
					)
				}),
			}
			if note != "" {
				rows = append(rows, vspace(3), layout.Rigid(label(u.th, 12, note, colSub, font.Normal).Layout))
			}
			for _, f := range files {
				rows = append(rows, vspace(10), layout.Rigid(func(gtx C) D { return u.fileProgress(gtx, f) }))
			}
			if more > 0 {
				rows = append(rows, vspace(8), layout.Rigid(label(u.th, 12, fmt.Sprintf("외 %d개", more), colSub, font.Normal).Layout))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		})
	})
}

// fileProgress is one upload: name and percent over a thin meter.
func (u *UI) fileProgress(gtx C, f mount.Transfer) D {
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					l := label(u.th, 12, path.Base(f.Name), colText, font.Normal)
					l.MaxLines = 1
					return l.Layout(gtx)
				}),
				hspace(8),
				layout.Rigid(label(u.th, 12, fmt.Sprintf("%d%%", f.Percent), colSub, font.Medium).Layout),
			)
		}),
		vspace(4),
		layout.Rigid(func(gtx C) D { return usageBar(gtx, float64(f.Percent)/100, colAccent) }),
	)
}

func (u *UI) active() bool {
	act := u.activity()
	return speedText(act) != "" || act.Uploads > 0
}

// usage shows the account's space: a meter when there is a limit,
// otherwise just what is used.
func (u *UI) usage(gtx C, q provider.Quota) D {
	if q.Unlimited() {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(label(u.th, 12, humanBytes(q.Used)+" 사용 중 · 용량 제한 없음", colSub, font.Normal).Layout),
			vspace(3),
			// the server reports no limit and no free space, so Explorer gets rclone's huge default
			layout.Rigid(label(u.th, 11, "서버에 할당량이 없어 탐색기에는 용량이 아주 크게 보일 수 있어요.", colFaint, font.Normal).Layout),
		)
	}
	frac := float64(q.Used) / float64(q.Total)
	fill := colAccent
	if frac >= 0.9 {
		fill = colDanger
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D { return usageBar(gtx, frac, fill) }),
		vspace(6),
		layout.Rigid(func(gtx C) D {
			return layout.Flex{}.Layout(gtx,
				layout.Flexed(1, label(u.th, 12, humanBytes(q.Used)+" / "+humanBytes(q.Total)+" 사용", colSub, font.Normal).Layout),
				layout.Rigid(label(u.th, 12, humanBytes(q.Free())+" 남음", colText, font.Medium).Layout),
			)
		}),
	)
}

func (u *UI) account(gtx C, a core.AccountView) D {
	aw := u.accts[a.ID]
	if aw == nil {
		aw = &acctWidgets{}
		u.accts[a.ID] = aw
	}
	u.mu.Lock()
	busy := u.busy[a.ID]
	u.mu.Unlock()

	if aw.refresh.Clicked(gtx) && !a.Loading {
		go u.svc.Refresh(context.Background(), a.ID)
	}
	if aw.signIn.Clicked(gtx) && !aw.signingIn.Load() {
		aw.signingIn.Store(true)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			u.svc.SignInAgain(ctx, a.ID) // a failure shows on the account
			aw.signingIn.Store(false)
			u.invalidate()
		}()
	}
	if aw.logout.Clicked(gtx) && !busy {
		if time.Now().Before(aw.confirmUntil) {
			u.logout(a.ID)
		} else {
			// first click arms the button; it disarms itself after a few seconds
			aw.confirmUntil = time.Now().Add(4 * time.Second)
			go func() { time.Sleep(4 * time.Second); u.invalidate() }()
		}
	}
	logoutText := "로그아웃"
	if time.Now().Before(aw.confirmUntil) {
		logoutText = "정말 로그아웃"
	}

	host := strings.TrimPrefix(strings.TrimPrefix(a.Server, "https://"), "http://")
	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 16, a.Title, colText, font.Bold).Layout),
							vspace(2),
							layout.Rigid(label(u.th, 13, displayUser(a)+" · "+host, colSub, font.Normal).Layout),
						)
					}),
					layout.Rigid(smallButton(u.th, &aw.refresh, "새로고침", colSub).Layout),
					hspace(6),
					layout.Rigid(smallButton(u.th, &aw.logout, logoutText, colDanger).Layout),
				)
			}),
		}
		if a.Quota != nil {
			q := *a.Quota
			children = append(children, vspace(14), layout.Rigid(func(gtx C) D { return u.usage(gtx, q) }))
		}
		if a.Err != "" {
			children = append(children, vspace(12), layout.Rigid(label(u.th, 13, a.Err, colDanger, font.Normal).Layout))
			if a.NeedsSignIn {
				txt := "다시 로그인"
				if aw.signingIn.Load() {
					txt = "브라우저에서 로그인해 주세요…"
				}
				children = append(children, vspace(10), layout.Rigid(func(gtx C) D {
					b := smallButton(u.th, &aw.signIn, txt, colOnAccent)
					b.Background = colAccent
					return b.Layout(gtx)
				}))
			}
		}
		if a.Loading && len(a.Drives) == 0 {
			children = append(children, vspace(12), layout.Rigid(label(u.th, 13, "공간 목록을 불러오는 중…", colSub, font.Normal).Layout))
		}
		for i, d := range a.Drives {
			if i == 0 {
				children = append(children, vspace(8))
			}
			children = append(children, layout.Rigid(func(gtx C) D { return u.driveRow(gtx, a.ID, d) }))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

func displayUser(a core.AccountView) string {
	if a.Display != "" {
		return a.Display
	}
	return a.User
}

func (u *UI) driveRow(gtx C, acctID string, d core.DriveView) D {
	key := acctID + "/" + d.ID
	dw := u.drives[key]
	if dw == nil {
		dw = &driveWidgets{}
		u.drives[key] = dw
	}
	on := d.State == core.Mounted || d.State == core.Connecting || d.State == core.Suspended
	if dw.sw.Update(gtx) {
		if dw.sw.Value {
			u.svc.Mount(acctID, d.ID)
		} else {
			u.svc.Unmount(acctID, d.ID)
		}
	} else {
		dw.sw.Value = on
	}
	if dw.open.Clicked(gtx) && d.State == core.Mounted {
		browser.OpenPath(d.Target)
	}
	letters := mount.UsesDriveLetters()
	settings := letters || d.InstantOK // anything to choose for this drive
	if settings && dw.letter.Clicked(gtx) {
		switch {
		case on:
			dw.note = "연결을 끈 뒤에 바꿀 수 있어요"
		case dw.picking:
			dw.picking = false
		default:
			dw.picking, dw.note = true, ""
			dw.free = u.svc.AvailableTargets(acctID, d.ID)
		}
	}
	if on {
		dw.picking = false
	} else if dw.note != "" && !dw.picking {
		dw.note = ""
	}
	if dw.picking {
		for _, v := range append([]string{""}, dw.free...) {
			if dw.chip(v).Clicked(gtx) {
				if err := u.svc.SetTarget(acctID, d.ID, v); err != nil {
					dw.note = err.Error()
				}
				dw.picking = false
			}
		}
		for _, m := range []core.Mode{core.Stable, core.Instant} {
			if dw.modeBtn(m).Clicked(gtx) {
				if err := u.svc.SetMode(acctID, d.ID, m); err != nil {
					dw.note = err.Error()
				}
			}
		}
	}

	status, col := "연결 안 됨", colSub
	switch d.State {
	case core.Connecting:
		status, col = "연결 중…", colAccent
	case core.Mounted:
		status, col = d.Target+" 에 연결됨", colOK
		if d.Mode == core.Instant && d.InstantOK {
			status += " · 즉시 반영"
		}
		if d.Uploads > 0 {
			status, col = fmt.Sprintf("%s 에 연결됨 · 업로드 %d개 남음", d.Target, d.Uploads), colAccent
		}
	case core.Failed:
		status, col = "오류: "+d.Err, colDanger
	case core.Suspended:
		status, col = "서버 응답 없음 · 돌아오면 다시 켜요", colDanger
	}

	row := func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
							layout.Rigid(label(u.th, 15, d.Name, colText, font.Medium).Layout),
							hspace(6),
							layout.Rigid(label(u.th, 12, kindName(d.Kind), colFaint, font.Normal).Layout),
						)
					}),
					vspace(3),
					layout.Rigid(func(gtx C) D {
						l := label(u.th, 12, status, col, font.Normal)
						l.MaxLines = 2
						return l.Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx C) D {
				if d.State != core.Mounted {
					return D{}
				}
				return layout.Inset{Right: 8}.Layout(gtx, smallButton(u.th, &dw.open, "열기", colAccent).Layout)
			}),
			layout.Rigid(func(gtx C) D {
				if !settings {
					return D{}
				}
				txt, fg := "설정", colSub
				if letters {
					txt = "자동"
					if d.Preferred != "" {
						txt, fg = d.Preferred, colText
					}
				}
				if d.InstantOK && d.Mode == core.Instant {
					txt += " · 즉시"
				}
				if on {
					fg = colFaint
				}
				b := smallButton(u.th, &dw.letter, txt+" ▾", fg)
				if dw.picking {
					b.Background, b.Color = colAccentDim, colAccent
				}
				return layout.Inset{Right: 10}.Layout(gtx, b.Layout)
			}),
			layout.Rigid(toggle(u.th, &dw.sw, d.Name+" 연결").Layout),
		)
	}

	children := []layout.FlexChild{layout.Rigid(row)}
	if dw.note != "" {
		children = append(children, vspace(6), layout.Rigid(label(u.th, 12, dw.note, colSub, font.Normal).Layout))
	}
	if dw.picking {
		children = append(children, vspace(10), layout.Rigid(func(gtx C) D { return u.drivePanel(gtx, dw, d, letters) }))
	}
	return layout.Inset{Top: 10, Bottom: 10}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// letterPicker shows "자동" and every free letter as a grid of chips.
func (u *UI) letterPicker(gtx C, dw *driveWidgets, selected string) D {
	items := append([]string{""}, dw.free...)
	chipW, gap := gtx.Dp(54), gtx.Dp(6)
	perRow := max(1, (gtx.Constraints.Max.X+gap)/(chipW+gap))

	chip := func(v string) layout.Widget {
		return func(gtx C) D {
			txt := v
			if v == "" {
				txt = "자동"
			}
			b := smallButton(u.th, dw.chip(v), txt, colText)
			if v == selected {
				b.Background, b.Color = colAccent, colOnAccent
			}
			gtx.Constraints.Min.X, gtx.Constraints.Max.X = chipW, chipW
			return b.Layout(gtx)
		}
	}

	rows := []layout.FlexChild{
		layout.Rigid(label(u.th, 12, "드라이브 문자 · 다음 연결부터 적용", colSub, font.Normal).Layout),
		vspace(8),
	}
	for i := 0; i < len(items); i += perRow {
		line := items[i:min(i+perRow, len(items))]
		rows = append(rows, layout.Rigid(func(gtx C) D {
			var cells []layout.FlexChild
			for j, v := range line {
				if j > 0 {
					cells = append(cells, layout.Rigid(layout.Spacer{Width: unit.Dp(6)}.Layout))
				}
				cells = append(cells, layout.Rigid(chip(v)))
			}
			return layout.Flex{}.Layout(gtx, cells...)
		}), vspace(6))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
}

func kindName(k provider.DriveKind) string {
	switch k {
	case provider.KindPersonal:
		return "개인"
	case provider.KindProject:
		return "프로젝트"
	case provider.KindShares:
		return "공유받음"
	}
	return ""
}

func (u *UI) logout(id string) {
	u.mu.Lock()
	u.busy[id] = true
	u.mu.Unlock()
	go func() {
		u.svc.Logout(context.Background(), id)
		u.mu.Lock()
		delete(u.busy, id)
		u.mu.Unlock()
		u.invalidate()
	}()
}

func (u *UI) settings(gtx C) D {
	if u.autoStart.Update(gtx) {
		if err := autostart.Set(u.autoStart.Value); err != nil {
			u.autoStart.Value = !u.autoStart.Value
			u.autoErr = err.Error()
		} else {
			u.autoErr = ""
		}
	}
	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 15, "컴퓨터를 켜면 자동 실행", colText, font.Medium).Layout),
							vspace(3),
							layout.Rigid(label(u.th, 12, "트레이로 시작해 켜 둔 드라이브를 다시 연결해요", colSub, font.Normal).Layout),
						)
					}),
					hspace(12),
					layout.Rigid(toggle(u.th, &u.autoStart, "자동 실행").Layout),
				)
			}),
		}
		if u.autoErr != "" {
			children = append(children, vspace(8), layout.Rigid(label(u.th, 12, u.autoErr, colDanger, font.Normal).Layout))
		}
		children = append(children, vspace(16), layout.Rigid(u.suspendRow))
		children = append(children, vspace(16), layout.Rigid(u.themeRow))
		children = append(children,
			vspace(16),
			layout.Rigid(func(gtx C) D { return u.footer(gtx) }),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// footer explains that closing the window keeps the app running, and
// offers to quit it for real.
func (u *UI) footer(gtx C) D {
	mounted := u.svc.MountedCount()
	armed := time.Now().Before(u.quitArmed)
	if u.quitBtn.Clicked(gtx) {
		if mounted == 0 || armed {
			go u.quit()
		} else {
			u.quitArmed = time.Now().Add(4 * time.Second)
			armed = true
			go func() { time.Sleep(4 * time.Second); u.invalidate() }()
		}
	}
	text := "앱 종료"
	if armed && mounted > 0 {
		text = fmt.Sprintf("연결 %d개 끊고 종료", mounted)
		if n := u.activity().Uploads; n > 0 {
			// unfinished uploads resume on the next start, but say so
			text = fmt.Sprintf("업로드 %d개 남음 · 그래도 종료", n)
		}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(label(u.th, 12, "창을 닫아도 트레이에서 계속 동작해요.", colFaint, font.Normal).Layout),
				vspace(2),
				layout.Rigid(label(u.th, 12, "트레이 아이콘 오른쪽 클릭: 메뉴", colFaint, font.Normal).Layout),
			)
		}),
		hspace(10),
		layout.Rigid(func(gtx C) D {
			if u.logBtn.Clicked(gtx) && u.LogPath != "" {
				browser.OpenPath(filepath.Dir(u.LogPath))
			}
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(gtx,
				layout.Rigid(smallButton(u.th, &u.logBtn, "로그 폴더", colSub).Layout),
				vspace(6),
				layout.Rigid(smallButton(u.th, &u.quitBtn, text, colDanger).Layout),
			)
		}),
	)
}
