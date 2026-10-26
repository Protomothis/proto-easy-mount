// Package tray shows the app in the system tray (Windows notification area,
// macOS menu bar) with a menu to open the window and toggle drives.
package tray

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"fyne.io/systray"

	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/icon"
	"github.com/protomothis/proto-easy-mount/internal/release"
)

// Tray mirrors the service state in the tray menu.
type Tray struct {
	svc    *core.Service
	show   func()
	quit   func()
	icons  [2][]byte // idle, active
	mu     sync.Mutex
	sig    string             // last menu signature, to skip needless rebuilds
	cancel context.CancelFunc // stops click listeners of the previous menu
	active int
	ready  bool // systray is up; before that the menu can't be touched
	closed bool // the tray icon is gone; updates would only log errors

	// Version is shown in the menu; NewVersion, when set, reports a newer
	// release ("" when there is none). Set both before OnReady.
	Version    string
	NewVersion func() string
}

// New prepares a tray; call OnReady from systray's ready callback.
func New(svc *core.Service, show, quit func()) *Tray {
	t := &Tray{svc: svc, show: show, quit: quit, active: -1}
	if runtime.GOOS == "windows" {
		t.icons = [2][]byte{icon.ICO(false), icon.ICO(true)}
	} else {
		t.icons = [2][]byte{icon.PNG(64, false), icon.PNG(64, true)}
	}
	return t
}

// OnReady builds the menu and follows service changes.
func (t *Tray) OnReady() {
	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()
	systray.SetTitle("")
	systray.SetOnTapped(t.show) // left click opens the window; right click the menu
	t.update()
	t.svc.Subscribe(t.update)
}

func (t *Tray) update() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || !t.ready {
		return
	}

	n := t.svc.MountedCount()
	if a := min(n, 1); a != t.active {
		t.active = a
		systray.SetIcon(t.icons[a])
	}
	if n > 0 {
		tip := fmt.Sprintf("Proto Easy Mount · %d개 연결됨", n)
		if up := t.svc.Activity().Uploads; up > 0 {
			tip += fmt.Sprintf(" · 업로드 %d개", up)
		}
		systray.SetTooltip(tip + "\n오른쪽 클릭: 메뉴")
	} else {
		systray.SetTooltip("Proto Easy Mount\n오른쪽 클릭: 메뉴")
	}

	accts := t.svc.Snapshot()
	newer := ""
	if t.NewVersion != nil {
		newer = t.NewVersion()
	}
	if sig := signature(accts) + "|" + newer; sig != t.sig {
		t.sig = sig
		t.rebuild(accts, newer)
	}
}

func signature(accts []core.AccountView) string {
	var b strings.Builder
	for _, a := range accts {
		fmt.Fprintf(&b, "%s|%v|", a.ID, a.Loading)
		for _, d := range a.Drives {
			fmt.Fprintf(&b, "%s:%d:%s;", d.ID, d.State, d.Target)
		}
	}
	return b.String()
}

// rebuild replaces the whole menu; systray has no insert-at API.
func (t *Tray) rebuild(accts []core.AccountView, newer string) {
	if t.cancel != nil {
		t.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	systray.ResetMenu()

	on := func(item *systray.MenuItem, fn func()) {
		go func() {
			for {
				select {
				case <-item.ClickedCh:
					fn()
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	on(systray.AddMenuItem("창 열기", ""), t.show)
	systray.AddSeparator()

	any := false
	for _, a := range accts {
		head := systray.AddMenuItem(a.Title+" · "+a.User, a.Server)
		head.Disable()
		if a.Loading && len(a.Drives) == 0 {
			systray.AddMenuItem("  불러오는 중…", "").Disable()
		}
		for _, d := range a.Drives {
			item := systray.AddMenuItemCheckbox("  "+DriveLabel(d), "", d.State == core.Mounted || d.State == core.Connecting)
			acct, drive, mounted := a.ID, d.ID, d.State == core.Mounted || d.State == core.Connecting
			on(item, func() {
				if mounted {
					t.svc.Unmount(acct, drive)
				} else {
					t.svc.Mount(acct, drive)
				}
			})
			any = true
		}
		systray.AddSeparator()
	}
	if any {
		on(systray.AddMenuItem("모두 연결 해제", ""), func() {
			for _, a := range t.svc.Snapshot() {
				for _, d := range a.Drives {
					t.svc.Unmount(a.ID, d.ID)
				}
			}
		})
	}
	systray.AddSeparator()
	if newer != "" {
		on(systray.AddMenuItem("새 버전 "+newer+" · 업데이트…", "창에서 업데이트합니다"), t.show)
	} else {
		systray.AddMenuItem("Proto Easy Mount "+t.Version, "").Disable()
	}
	on(systray.AddMenuItem("GitHub", release.Repo), func() { browser.Open(release.Repo) })
	on(systray.AddMenuItem("♥ 후원하기", release.Sponsor), func() { browser.Open(release.Sponsor) })
	systray.AddSeparator()
	on(systray.AddMenuItem("앱 종료", "드라이브 연결을 끊고 앱을 끕니다"), t.quit)
}

// Refresh redraws the menu, e.g. when a new version turns up.
func (t *Tray) Refresh() { t.update() }

// Close stops following the service; call when systray exits, before
// drives are unmounted.
func (t *Tray) Close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
}

// DriveLabel describes a drive and its state in one line.
func DriveLabel(d core.DriveView) string {
	name := d.Name
	switch d.State {
	case core.Connecting:
		return name + " (연결 중…)"
	case core.Mounted:
		return name + " (" + d.Target + ")"
	case core.Failed:
		return name + " (오류)"
	}
	return name
}
