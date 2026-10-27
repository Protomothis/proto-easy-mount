package gui

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/protomothis/proto-easy-mount/internal/browser"
	"github.com/protomothis/proto-easy-mount/internal/release"
	"github.com/protomothis/proto-easy-mount/internal/update"
)

const noticesURL = release.Repo + "/blob/main/THIRD-PARTY-NOTICES.md"

const checkEvery = 6 * time.Hour

type updPhase int

const (
	updIdle updPhase = iota
	updChecking
	updDownloading
	updWaiting // downloaded; uploads must finish before the app quits
	updInstalling
)

// updater is the update state; it is shared with goroutines, so it has
// its own lock.
type updater struct {
	mu      sync.Mutex
	phase   updPhase
	avail   *update.Available
	path    string // the downloaded installer
	err     string
	checked time.Time
}

type aboutWidgets struct {
	check, install, notes, repo, sponsor, licenses widget.Clickable
	testChannel, autoCheckSw                       widget.Bool
}

// NewVersion returns the tag of a release newer than the running one, or "".
func (u *UI) NewVersion() string {
	u.upd.mu.Lock()
	defer u.upd.mu.Unlock()
	if u.upd.avail == nil {
		return ""
	}
	return u.upd.avail.Version
}

// WatchUpdates checks for a new release shortly after start and then every
// few hours until ctx ends.
func (u *UI) WatchUpdates(ctx context.Context) {
	t := time.NewTimer(15 * time.Second) // let the drives come back first
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if u.autoCheck() {
				u.checkUpdate()
			}
			t.Reset(checkEvery)
		}
	}
}

func (u *UI) checkUpdate() {
	u.upd.mu.Lock()
	if u.upd.phase != updIdle {
		u.upd.mu.Unlock()
		return
	}
	u.upd.phase, u.upd.err = updChecking, ""
	u.upd.mu.Unlock()
	u.changed()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	a, err := update.Check(ctx, u.version, u.svc.TestUpdates())
	cancel()

	u.upd.mu.Lock()
	u.upd.phase, u.upd.checked = updIdle, time.Now()
	if err != nil {
		u.upd.err = "업데이트를 확인하지 못했어요"
		slog.Warn(fmt.Sprintf("업데이트 확인 실패: %v", err))
	} else {
		if a != nil && (u.upd.avail == nil || u.upd.avail.Version != a.Version) {
			slog.Info(fmt.Sprintf("새 버전 %s 있음 (지금 %s)", a.Version, u.version))
		}
		u.upd.avail = a
	}
	u.upd.mu.Unlock()
	u.changed()
}

// startUpdate downloads the installer and installs it once no uploads are
// left: the app quits (unmounting the drives), setup replaces it and starts
// it again.
func (u *UI) startUpdate() {
	u.upd.mu.Lock()
	a := u.upd.avail
	if a == nil || u.upd.phase != updIdle {
		u.upd.mu.Unlock()
		return
	}
	u.upd.phase, u.upd.err = updDownloading, ""
	u.upd.mu.Unlock()
	u.changed()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	path, err := a.Download(ctx, filepath.Join(os.TempDir(), "proto-easy-mount-update"))
	cancel()
	u.upd.mu.Lock()
	if err != nil {
		u.upd.phase, u.upd.err = updIdle, "설치 파일을 받지 못했어요"
		u.upd.mu.Unlock()
		slog.Error(fmt.Sprintf("업데이트 받기 실패 %s: %v", a.Version, err))
		u.changed()
		return
	}
	u.upd.phase, u.upd.path = updWaiting, path
	u.upd.mu.Unlock()
	slog.Info(fmt.Sprintf("업데이트 %s 받음, 업로드가 끝나면 설치", a.Version))
	u.changed()

	for u.activity().Uploads > 0 {
		time.Sleep(2 * time.Second)
	}
	u.upd.mu.Lock()
	u.upd.phase = updInstalling
	u.upd.mu.Unlock()
	u.changed()
	slog.Info(fmt.Sprintf("업데이트 %s 설치를 위해 종료", a.Version))
	if u.Install != nil {
		u.Install(path)
	}
}

// changed redraws the window and lets the tray show the update state.
func (u *UI) changed() {
	u.invalidate()
	if u.OnUpdate != nil {
		u.OnUpdate()
	}
}

// about is the 정보 card: version and updates, project links, licenses.
func (u *UI) about(gtx C) D {
	aw := &u.aboutW
	u.upd.mu.Lock()
	phase, avail, errText, checked := u.upd.phase, u.upd.avail, u.upd.err, u.upd.checked
	u.upd.mu.Unlock()

	if aw.testChannel.Update(gtx) {
		on := aw.testChannel.Value
		go func() {
			u.svc.SetTestUpdates(on)
			u.upd.mu.Lock()
			u.upd.avail = nil // the other channel may offer something else
			u.upd.mu.Unlock()
			u.checkUpdate()
		}()
	}
	if aw.check.Clicked(gtx) {
		go u.checkUpdate()
	}
	if aw.install.Clicked(gtx) {
		go u.startUpdate()
	}
	if aw.notes.Clicked(gtx) && avail != nil {
		browser.Open(avail.Page)
	}
	if aw.repo.Clicked(gtx) {
		browser.Open(release.Repo)
	}
	if aw.sponsor.Clicked(gtx) {
		browser.Open(release.Sponsor)
	}
	if aw.licenses.Clicked(gtx) {
		u.openNotices()
	}

	status, statusCol := "최신 버전이에요", colSub
	switch {
	case phase == updChecking:
		status = "업데이트 확인 중…"
	case phase == updDownloading:
		status = "새 버전 받는 중…"
	case phase == updWaiting:
		status, statusCol = "업로드가 끝나면 설치하고 다시 시작해요", colAccent
	case phase == updInstalling:
		status, statusCol = "설치하는 중… 잠시 후 다시 시작해요", colAccent
	case errText != "":
		status, statusCol = errText, colDanger
	case avail != nil:
		status, statusCol = fmt.Sprintf("새 버전 %s 이 있어요", avail.Version), colAccent
	case checked.IsZero():
		status = "아직 확인하지 않았어요"
	}

	return card(gtx, func(gtx C) D {
		children := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 15, "버전 "+displayVersion(u.version), colText, font.Medium).Layout),
							vspace(3),
							layout.Rigid(label(u.th, 12, status, statusCol, font.Normal).Layout),
						)
					}),
					hspace(10),
					layout.Rigid(func(gtx C) D {
						if avail != nil && phase == updIdle {
							return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(smallButton(u.th, &aw.notes, "변경 내용", colSub).Layout),
								hspace(6),
								layout.Rigid(func(gtx C) D {
									b := smallButton(u.th, &aw.install, "업데이트", colOnAccent)
									b.Background = colAccent
									return b.Layout(gtx)
								}),
							)
						}
						if phase != updIdle {
							return D{}
						}
						return smallButton(u.th, &aw.check, "업데이트 확인", colSub).Layout(gtx)
					}),
				)
			}),
			vspace(14),
			layout.Rigid(func(gtx C) D {
				aw.testChannel.Value = u.svc.TestUpdates()
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 14, "테스트 버전도 받기", colText, font.Normal).Layout),
							vspace(2),
							layout.Rigid(label(u.th, 12, "정식 출시 전 버전(rc)으로도 업데이트해요", colSub, font.Normal).Layout),
						)
					}),
					hspace(12),
					layout.Rigid(toggle(u.th, &aw.testChannel, "테스트 버전").Layout),
				)
			}),
			vspace(14),
			layout.Rigid(func(gtx C) D {
				if aw.autoCheckSw.Update(gtx) {
					u.svc.SetUpdateCheck(aw.autoCheckSw.Value)
				}
				aw.autoCheckSw.Value = u.autoCheck()
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(u.th, 14, "업데이트 자동 확인", colText, font.Normal).Layout),
							vspace(2),
							layout.Rigid(label(u.th, 12, "6시간마다 GitHub에 새 버전을 물어봐요", colSub, font.Normal).Layout),
						)
					}),
					hspace(12),
					layout.Rigid(toggle(u.th, &aw.autoCheckSw, "업데이트 자동 확인").Layout),
				)
			}),
			vspace(14),
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(smallButton(u.th, &aw.repo, "GitHub", colSub).Layout),
					hspace(6),
					layout.Rigid(smallButton(u.th, &aw.sponsor, "♥ 후원하기", colDanger).Layout),
					hspace(6),
					layout.Rigid(smallButton(u.th, &aw.licenses, "오픈소스 라이선스", colSub).Layout),
				)
			}),
			vspace(10),
			layout.Rigid(label(u.th, 12, "GPL-3.0 · © Protomothis", colFaint, font.Normal).Layout),
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
}

// openNotices opens the third-party notices installed next to the app, or
// the copy in the repository for builds run from source.
func (u *UI) openNotices() {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), "THIRD-PARTY-NOTICES.txt")
		if _, err := os.Stat(p); err == nil {
			browser.OpenPath(p)
			return
		}
	}
	browser.Open(noticesURL)
}

// displayVersion writes a version the way releases are tagged.
func displayVersion(v string) string {
	if v == "" || v[0] == 'v' {
		return v
	}
	return "v" + v
}

// autoCheck is whether to ask GitHub for new versions every few hours: the
// choice made in the app, else the installer's (privacy: this is the only
// request to anyone but the user's own servers).
func (u *UI) autoCheck() bool {
	if on := u.svc.UpdateCheck(); on != nil {
		return *on
	}
	return update.InstallerDefault()
}
