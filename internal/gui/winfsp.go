package gui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/winfspsetup"
)

// Replaced by tests.
var (
	winfspNeeded    = winfspsetup.Needed
	winfspInstalled = winfspsetup.Installed
	winfspInstall   = winfspsetup.Install
)

// driverSetup tracks the WinFsp driver Windows mounts need. Without it the
// window shows only the setup card, so drives aren't offered before they
// can work.
type driverSetup struct {
	mu      sync.Mutex
	ok      bool
	checked time.Time
	busy    bool
	phase   winfspsetup.Phase
	err     string

	install, page widget.Clickable
}

// driverReady reports whether mounts can work; while not, it looks again
// every couple of seconds (someone may install WinFsp by hand).
func (u *UI) driverReady(gtx C) bool {
	if !winfspNeeded {
		return true
	}
	s := &u.driver
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ok {
		return true
	}
	if !s.busy && time.Since(s.checked) >= 2*time.Second {
		s.checked = time.Now()
		s.ok = winfspInstalled()
		if s.ok {
			go u.svc.RefreshAll(context.Background()) // drives left connected come back
			return true
		}
	}
	gtx.Execute(op.InvalidateCmd{At: s.checked.Add(2 * time.Second)})
	return false
}

func (u *UI) installDriver() {
	s := &u.driver
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy, s.err = true, ""
	s.mu.Unlock()
	u.invalidate()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	err := winfspInstall(ctx, func(p winfspsetup.Phase) {
		s.mu.Lock()
		s.phase = p
		s.mu.Unlock()
		u.invalidate()
	})
	s.mu.Lock()
	s.busy = false
	switch {
	case errors.Is(err, winfspsetup.ErrCancelled):
		s.err = "설치를 취소했어요. 드라이브를 연결하려면 WinFsp가 필요해요."
	case err != nil:
		s.err = "설치하지 못했어요. 다시 시도하거나 직접 설치해 주세요."
		slog.Error(fmt.Sprintf("WinFsp 설치 실패: %v", err))
	default:
		slog.Info("WinFsp " + winfspsetup.Version + " 설치됨")
		s.checked = time.Time{} // look again on the next frame
	}
	s.mu.Unlock()
	u.invalidate()
}

// driverCard asks to install WinFsp before anything else.
func (u *UI) driverCard(gtx C) D {
	s := &u.driver
	if s.install.Clicked(gtx) {
		go u.installDriver()
	}
	if s.page.Clicked(gtx) {
		browser.Open(winfspsetup.Page)
	}
	s.mu.Lock()
	busy, phase, errText := s.busy, s.phase, s.err
	s.mu.Unlock()

	btnText := "WinFsp 설치"
	switch {
	case busy && phase == winfspsetup.Downloading:
		btnText = "받는 중…"
	case busy:
		btnText = "설치 중… 관리자 확인 창에서 [예]"
	case errText != "":
		btnText = "다시 시도"
	}
	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(label(u.th, 18, "먼저 WinFsp를 설치해 주세요", colText, font.Bold).Layout),
			vspace(6),
			layout.Rigid(label(u.th, 13, "Windows에서 드라이브를 연결하려면 WinFsp 드라이버가 필요해요. "+
				"공식 설치 파일("+winfspsetup.Version+")을 받아 확인한 뒤 설치하고, 끝나면 바로 이어서 쓸 수 있어요.", colSub, font.Normal).Layout),
			vspace(16),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				b := primaryButton(u.th, &s.install, btnText)
				if busy {
					gtx = gtx.Disabled()
				}
				return b.Layout(gtx)
			}),
		}
		if errText != "" {
			children = append(children, vspace(10), layout.Rigid(label(u.th, 12, errText, colDanger, font.Normal).Layout))
		}
		children = append(children,
			vspace(10),
			layout.Rigid(func(gtx C) D {
				b := smallButton(u.th, &s.page, "직접 설치하기 (winfsp.dev)", colSub)
				b.Background = colCard
				b.Inset.Left = 2
				return b.Layout(gtx)
			}),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}
