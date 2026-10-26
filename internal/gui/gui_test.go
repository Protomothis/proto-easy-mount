package gui

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"github.com/zalando/go-keyring"

	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/icon"
	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/store"
	"github.com/protomothis/proto-easy-mount/internal/update"
)

type fakeProvider struct{}

func init() { provider.Register(fakeProvider{}) }

func (fakeProvider) Name() string               { return "fake" }
func (fakeProvider) Title() string              { return "예시 서비스" }
func (fakeProvider) Description() string        { return "화면 확인용 가짜 서비스" }
func (fakeProvider) AddressHint() string        { return "예: example.com" }
func (fakeProvider) Options() []provider.Option { return nil }
func (fakeProvider) Resolve(_ context.Context, in string) (provider.Server, error) {
	return provider.Server{URL: "https://" + in}, nil
}
func (fakeProvider) Login(context.Context, provider.Env, provider.LoginOptions) (provider.Account, provider.Secret, error) {
	return provider.Account{}, provider.Secret{}, nil
}
func (fakeProvider) Refresh(_ context.Context, _ provider.Account, s provider.Secret) (provider.Secret, error) {
	return s, nil
}
func (fakeProvider) Logout(context.Context, provider.Account, provider.Secret) error { return nil }
func (fakeProvider) Drives(context.Context, provider.Account, provider.Secret) ([]provider.Drive, error) {
	return []provider.Drive{
		{ID: "me", Name: "김프로토", Kind: provider.KindPersonal, Quota: &provider.Quota{Used: 132 << 30, Total: 500 << 30}},
		{ID: "fam", Name: "가족 사진", Kind: provider.KindProject},
		{ID: "sh", Name: "Shares", Kind: provider.KindShares},
	}, nil
}
func (fakeProvider) Remote(provider.Env, provider.Account, provider.Drive) (provider.Remote, error) {
	return provider.Remote{}, nil
}

func newUI(t *testing.T, withAccount bool) *UI {
	keyring.MockInit()
	d := t.TempDir()
	st := &store.Store{ConfigDir: d + "/cfg", CacheDir: d + "/cache"}
	if withAccount {
		_, err := st.Save(provider.Account{Provider: "fake", Server: "https://cloud.example.com", User: "protomothis", Display: "김프로토"},
			provider.Secret{AccessToken: "x", Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	svc, err := core.New(st, core.Env(func(string) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	svc.RefreshAll(context.Background())
	u := New(svc, "0.1.0-dev", func() {})
	u.th = newTheme()
	u.logo = paint.NewImageOp(icon.Image(128, false))
	return u
}

// render lays the UI out once. With PEM_SNAPSHOT_DIR set it also draws it
// offscreen and saves a PNG, for looking at the design without running the app.
func render(t *testing.T, u *UI, name string) { renderWidth(t, u, name, 440) }

func renderWidth(t *testing.T, u *UI, name string, widthDp int) {
	const scale = 1.5
	size := image.Pt(int(float64(widthDp)*scale), int(720*scale))
	var ops op.Ops
	var router input.Router // an enabled input source, so widgets draw in their active colours
	frame := func() {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size),
			Now:         time.Now(),
			Source:      router.Source(),
		}
		paint.Fill(gtx.Ops, colBg)
		u.layout(gtx)
	}
	frame()

	dir := os.Getenv("PEM_SNAPSHOT_DIR")
	if dir == "" || name == "" {
		return
	}
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skipf("no offscreen GPU: %v", err)
	}
	defer w.Release()
	if err := w.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := w.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

func TestLayoutEmpty(t *testing.T) {
	render(t, newUI(t, false), "empty")
}

func TestLayoutAccount(t *testing.T) {
	render(t, newUI(t, true), "account")
}

func TestLayoutLetterPicker(t *testing.T) {
	u := newUI(t, true)
	acct := u.svc.Snapshot()[0]
	if err := u.svc.SetTarget(acct.ID, "fam", "P:"); err != nil {
		t.Fatal(err)
	}
	render(t, u, "") // creates the row widgets
	dw := u.drives[acct.ID+"/me"]
	dw.picking = true
	dw.free = []string{"E:", "F:", "G:", "H:", "K:", "M:", "N:", "Q:", "R:", "S:", "T:", "W:", "X:", "Y:", "Z:"}
	render(t, u, "letters")
}

func openCloud(t *testing.T) provider.Provider {
	p, err := provider.Get("opencloud")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLayoutAddressWithError(t *testing.T) {
	u := newUI(t, true)
	u.showAdd = true
	u.add.step, u.add.prov, u.add.showAdv = 1, openCloud(t), true
	u.add.address.SetText("nas.example.com")
	u.add.err = &provider.UserError{
		Msg:  "로그인 서버가 이 앱을 아직 몰라요.",
		Hint: "관리자에게 'OpenCloudDesktop' 클라이언트 등록을 요청하거나, 고급 설정에 받은 클라이언트 ID를 넣어 주세요.",
		Err:  errors.New("probe: 400 Bad Request"),
	}
	render(t, u, "add-error")
}

func TestLayoutUnlimitedQuota(t *testing.T) {
	u := newUI(t, true)
	acct := u.svc.Snapshot()[0]
	if acct.Quota == nil {
		t.Fatal("account quota missing")
	}
	q := provider.Quota{Used: 3 << 30}
	render(t, u, "") // build widgets
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 200))}
	if d := u.usage(gtx, q); d.Size.Y == 0 {
		t.Fatal("unlimited usage drew nothing")
	}
}

func uploading() core.Activity {
	return core.Activity{Up: 47_500_000, Uploads: 5, Files: []mount.Transfer{
		{Name: "videos/2026 가족 여행 - 제주도 전체 영상 원본 (4K).mp4", Up: true, Percent: 82},
		{Name: "프로젝트 자료.zip", Up: true, Percent: 37},
		{Name: "photo.jpg", Up: true, Percent: 5},
		{Name: "notes.txt", Up: true, Percent: 0},
	}}
}

// The activity strip must not squeeze the header (it once took the whole
// row and stacked the title one glyph per line).
func TestLayoutUploading(t *testing.T) {
	u := newUI(t, true)
	u.activity = uploading
	render(t, u, "uploading")
	renderWidth(t, u, "uploading-narrow", 360)
}

func TestLayoutSigningIn(t *testing.T) {
	u := newUI(t, false)
	u.add.step, u.add.prov = 1, openCloud(t)
	u.add.address.SetText("192.168.0.10:9200")
	u.add.phase = signingIn
	u.add.server = provider.Server{URL: "https://cloud.example.com", Product: "OpenCloud 7.2.4"}
	render(t, u, "signing-in")
}

// Clicking a service on step one moves to its address step.
func TestPickProviderAdvances(t *testing.T) {
	u := newUI(t, false)
	u.add.provBtn("opencloud").Click()
	render(t, u, "")
	if u.add.step != 1 || u.add.prov == nil || u.add.prov.Name() != "opencloud" {
		t.Fatalf("step=%d prov=%v", u.add.step, u.add.prov)
	}
	u.add.back.Click()
	render(t, u, "")
	if u.add.step != 0 {
		t.Fatalf("back did not return to step one")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0:                   "0 B",
		1536:                "1.5 KB",
		132 << 30:           "132 GB",
		5 << 40:             "5.0 TB",
		9223372036854775807: "8192 PB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func (fakeProvider) InstantSupported(context.Context, provider.Account, provider.Secret) bool {
	return true
}
func (fakeProvider) InstantFolder(d provider.Drive) string {
	if d.Kind == provider.KindShares {
		return ""
	}
	return "https://example/" + d.ID
}

// The drive settings panel offers the upload mode where the server allows
// it, and choosing a mode is remembered.
func TestDriveSettingsPanel(t *testing.T) {
	u := newUI(t, true)
	acct := u.svc.Snapshot()[0]
	var me, shares core.DriveView
	for _, d := range acct.Drives {
		switch d.ID {
		case "me":
			me = d
		case "sh":
			shares = d
		}
	}
	if !me.InstantOK || shares.InstantOK {
		t.Fatalf("InstantOK personal=%v shares=%v", me.InstantOK, shares.InstantOK)
	}
	render(t, u, "") // create row widgets
	dw := u.drives[acct.ID+"/me"]
	dw.picking = true
	dw.free = []string{"X:", "Y:", "Z:"}
	dw.modeBtn(core.Instant).Click()
	render(t, u, "")
	if m := u.svc.Snapshot()[0].Drives[0].Mode; m != core.Instant {
		t.Fatalf("mode after click = %q", m)
	}
	render(t, u, "drive-settings")
}

// Dark mode swaps every color; the main screens must stay readable.
func TestLayoutDark(t *testing.T) {
	u := newUI(t, true)
	u.setDark(true)
	defer u.setDark(false)
	if colBg == rgb(lightPalette[0]) || u.th.Palette.Bg != colBg {
		t.Fatal("dark palette not applied")
	}
	u.activity = uploading
	render(t, u, "dark-uploading")
	u.activity = func() core.Activity { return core.Activity{} }
	render(t, u, "dark-account")
	render(t, newUI(t, false), "dark-empty")
}

// The 정보 card offers a found update next to the version.
func TestLayoutAboutUpdate(t *testing.T) {
	u := newUI(t, false)
	u.upd.avail = &update.Available{Version: "v0.2.0", Page: "https://x"}
	render(t, u, "about-update")
	if u.NewVersion() != "v0.2.0" {
		t.Fatal("tray would not see the update")
	}
}
