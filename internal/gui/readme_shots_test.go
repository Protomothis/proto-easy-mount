package gui

import (
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

	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/update"
)

// TestReadmeShots draws the parts of the window the README shows, each
// cropped to its own height (no scroll bar):
//
//	PEM_README_DIR=docs go test -tags cmount -run TestReadmeShots ./internal/gui/
func TestReadmeShots(t *testing.T) {
	dir := os.Getenv("PEM_README_DIR")
	if dir == "" {
		t.Skip("set PEM_README_DIR")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir) // relative to the repository
	}
	for _, dark := range []bool{false, true} {
		suffix := ""
		if dark {
			suffix = "-dark"
		}
		u := newUI(t, true)
		u.setDark(dark)
		u.activity = uploading
		a := u.svc.Snapshot()[0]
		looksReal(&a)
		shot(t, u, dir, "upload"+suffix, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(u.header), vspace(12), layout.Rigid(u.activityBar))
		})
		shot(t, u, dir, "account"+suffix, func(gtx C) D { return u.account(gtx, a) })

		// the letter and upload mode panel of the personal drive
		dw := u.drives[a.ID+"/me"]
		dw.picking = true
		dw.free = []string{"X:", "Y:", "Z:"}
		u.svc.SetMode(a.ID, "me", core.Instant)
		a = u.svc.Snapshot()[0]
		looksReal(&a)
		a.Drives[0].State, a.Drives[0].Target = core.Idle, ""
		shot(t, u, dir, "drive-settings"+suffix, func(gtx C) D { return u.account(gtx, a) })

		u.upd.avail = &update.Available{Version: "v0.2.0", Page: "https://x"}
		shot(t, u, dir, "about"+suffix, u.about)
		u.setDark(false)
	}
}

// shot lays w out at the window's width, measures it and saves it as a PNG
// exactly that tall, on the window background with the window's margins.
func shot(t *testing.T, u *UI, dir, name string, w layout.Widget) {
	t.Helper()
	const scale, widthDp = 2.0, 440
	width := int(widthDp * scale)
	var ops op.Ops
	var router input.Router // an enabled source, so widgets draw in their active colours
	frame := func(height int) D {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Constraints{Min: image.Pt(width, 0), Max: image.Pt(width, height)},
			Now:         time.Now(),
			Source:      router.Source(),
		}
		paint.Fill(gtx.Ops, colBg)
		return layout.UniformInset(16).Layout(gtx, w)
	}
	frame(10000) // creates widget state on first layout
	h := frame(10000).Size.Y
	frame(h)

	win, err := headless.NewWindow(width, h)
	if err != nil {
		t.Skipf("no offscreen GPU: %v", err)
	}
	defer win.Release()
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, img)
}

// looksReal dresses the sample account like a real one: an OpenCloud
// server with its personal space connected.
func looksReal(a *core.AccountView) {
	a.Title = "OpenCloud"
	a.Drives[0].State, a.Drives[0].Target = core.Mounted, "X:"
}
