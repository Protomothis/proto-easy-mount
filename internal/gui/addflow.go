package gui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/protomothis/proto-easy-mount/internal/core"
	"github.com/protomothis/proto-easy-mount/internal/provider"
)

type phase int

const (
	idle      phase = iota
	checking        // resolving the address
	signingIn       // waiting for the user to finish signing in
)

// addFlow is the two-step "connect storage" card: pick a service, then
// give its address and sign in.
type addFlow struct {
	u *UI

	// Shared with the login goroutine, guarded by u.mu.
	phase  phase
	server provider.Server
	err    *provider.UserError
	done   bool
	cancel context.CancelFunc

	// Window goroutine only.
	step     int // 0: pick a service, 1: address
	prov     provider.Provider
	provBtns map[string]*widget.Clickable
	address  widget.Editor
	opts     map[string]*widget.Editor
	showAdv  bool
	back     widget.Clickable
	close    widget.Clickable
	login    widget.Clickable
	stop     widget.Clickable
	adv      widget.Clickable
}

func newAddFlow(u *UI) *addFlow {
	f := &addFlow{u: u, provBtns: map[string]*widget.Clickable{}, opts: map[string]*widget.Editor{}}
	f.address.SingleLine = true
	f.address.Submit = true
	return f
}

func (f *addFlow) reset() {
	f.step, f.prov, f.showAdv = 0, nil, false
	f.address.SetText("")
	f.opts = map[string]*widget.Editor{}
	f.u.mu.Lock()
	f.err = nil
	f.u.mu.Unlock()
}

func (f *addFlow) optEditor(key string) *widget.Editor {
	e := f.opts[key]
	if e == nil {
		e = &widget.Editor{SingleLine: true, Submit: true}
		f.opts[key] = e
	}
	return e
}

func (f *addFlow) busy() bool {
	f.u.mu.Lock()
	defer f.u.mu.Unlock()
	return f.phase != idle
}

// update handles input; it runs before layout every frame.
func (f *addFlow) update(gtx C) {
	f.u.mu.Lock()
	done := f.done
	f.done = false
	f.u.mu.Unlock()
	if done {
		f.reset()
		f.u.showAdd = false
		return
	}

	if f.close.Clicked(gtx) && !f.busy() {
		f.reset()
		f.u.showAdd = false
	}
	if f.step == 0 {
		for _, p := range core.Providers() {
			if f.provBtn(p.Name()).Clicked(gtx) {
				f.prov, f.step = p, 1
				f.u.mu.Lock()
				f.err = nil
				f.u.mu.Unlock()
			}
		}
		return
	}

	if f.back.Clicked(gtx) && !f.busy() {
		f.step = 0
		f.u.mu.Lock()
		f.err = nil
		f.u.mu.Unlock()
	}
	if f.adv.Clicked(gtx) {
		f.showAdv = !f.showAdv
	}
	submit := f.login.Clicked(gtx)
	for _, ed := range append([]*widget.Editor{&f.address}, f.editors()...) {
		for {
			e, ok := ed.Update(gtx)
			if !ok {
				break
			}
			if _, ok := e.(widget.SubmitEvent); ok {
				submit = true
			}
		}
	}
	if f.stop.Clicked(gtx) {
		f.u.mu.Lock()
		if f.cancel != nil {
			f.cancel()
		}
		f.u.mu.Unlock()
	}
	if submit && !f.busy() {
		f.start()
	}
}

func (f *addFlow) provBtn(name string) *widget.Clickable {
	c := f.provBtns[name]
	if c == nil {
		c = new(widget.Clickable)
		f.provBtns[name] = c
	}
	return c
}

func (f *addFlow) editors() []*widget.Editor {
	if f.prov == nil {
		return nil
	}
	var eds []*widget.Editor
	for _, o := range f.prov.Options() {
		eds = append(eds, f.optEditor(f.prov.Name()+"/"+o.Key))
	}
	return eds
}

// start resolves the address, then signs in through the browser.
func (f *addFlow) start() {
	input := strings.TrimSpace(f.address.Text())
	name := f.prov.Name()
	values := map[string]string{}
	for _, o := range f.prov.Options() {
		if v := strings.TrimSpace(f.optEditor(name + "/" + o.Key).Text()); v != "" {
			values[o.Key] = v
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	f.u.mu.Lock()
	f.phase, f.err, f.cancel = checking, nil, cancel
	f.u.mu.Unlock()

	go func() {
		defer cancel()
		fail := func(err error) {
			f.u.mu.Lock()
			f.phase, f.cancel = idle, nil
			switch {
			case errors.Is(err, context.Canceled):
			case errors.Is(err, context.DeadlineExceeded):
				f.err = &provider.UserError{Msg: "로그인 시간이 지났어요.", Hint: "다시 시도해 주세요."}
			default:
				f.err = provider.AsUserError(err)
			}
			f.u.mu.Unlock()
			f.u.invalidate()
		}

		srv, err := f.u.svc.Resolve(ctx, name, input)
		if err != nil {
			fail(err)
			return
		}
		f.u.mu.Lock()
		f.phase, f.server = signingIn, srv
		f.u.mu.Unlock()
		f.u.invalidate()

		if _, err := f.u.svc.Login(ctx, name, srv, values); err != nil {
			fail(err)
			return
		}
		f.u.mu.Lock()
		f.phase, f.cancel, f.done = idle, nil, true
		f.u.mu.Unlock()
		f.u.invalidate()
	}()
}

func (f *addFlow) layout(gtx C, closable bool) D {
	return card(gtx, func(gtx C) D {
		if f.step == 0 {
			return f.pickLayout(gtx, closable)
		}
		return f.addressLayout(gtx, closable)
	})
}

// heading draws "title  ·  n / 2" with optional back and close buttons.
func (f *addFlow) heading(gtx C, title string, step int, back, closable bool) D {
	th := f.u.th
	busy := f.busy()
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			if !back {
				return D{}
			}
			b := smallButton(th, &f.back, "‹ 이전", colSub)
			if busy {
				b.Color = colFaint
			}
			return layout.Inset{Right: 10}.Layout(gtx, b.Layout)
		}),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
				layout.Rigid(label(th, 17, title, colText, font.Bold).Layout),
				hspace(8),
				layout.Rigid(label(th, 12, fmt.Sprintf("%d / 2", step), colFaint, font.Medium).Layout),
			)
		}),
		layout.Rigid(func(gtx C) D {
			if !closable || busy {
				return D{}
			}
			return smallButton(th, &f.close, "닫기", colSub).Layout(gtx)
		}),
	)
}

func (f *addFlow) pickLayout(gtx C, closable bool) D {
	th := f.u.th
	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return f.heading(gtx, "어떤 저장소를 연결할까요?", 1, false, closable) }),
		vspace(4),
		layout.Rigid(label(th, 13, "쓰고 있는 서비스를 골라 주세요.", colSub, font.Normal).Layout),
		vspace(12),
	}
	for i, p := range core.Providers() {
		if i > 0 {
			children = append(children, vspace(8))
		}
		children = append(children, layout.Rigid(func(gtx C) D { return f.providerRow(gtx, p) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// providerRow is a full-width tappable row: name, description and a chevron.
func (f *addFlow) providerRow(gtx C, p provider.Provider) D {
	th := f.u.th
	clk := f.provBtn(p.Name())
	return clk.Layout(gtx, func(gtx C) D {
		bg := colGhost
		if clk.Hovered() {
			bg = colAccentDim
		}
		return surface(gtx, bg, 12, func(gtx C) D {
			return layout.Inset{Top: 14, Bottom: 14, Left: 16, Right: 14}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(th, 15, p.Title(), colText, font.SemiBold).Layout),
							vspace(3),
							layout.Rigid(label(th, 12, p.Description(), colSub, font.Normal).Layout),
						)
					}),
					layout.Rigid(label(th, 20, "›", colFaint, font.Normal).Layout),
				)
			})
		})
	})
}

func (f *addFlow) addressLayout(gtx C, closable bool) D {
	th := f.u.th
	f.u.mu.Lock()
	ph, srv, uerr := f.phase, f.server, f.err
	f.u.mu.Unlock()

	var advanced []provider.Option
	for _, o := range f.prov.Options() {
		if o.Advanced {
			advanced = append(advanced, o)
		}
	}

	pw := passwordLogin(f.prov)
	intro, button, waiting := "서버 주소를 넣고 브라우저에서 로그인하면 끝이에요.", "브라우저로 로그인", "브라우저에서 로그인을 마쳐 주세요…"
	if pw {
		intro, button, waiting = "서버 주소와 계정을 넣고 로그인하세요.", "로그인", "로그인하는 중…"
	}

	children := []layout.FlexChild{
		layout.Rigid(func(gtx C) D { return f.heading(gtx, f.prov.Title()+" 연결", 2, true, closable) }),
		vspace(4),
		layout.Rigid(label(th, 13, intro, colSub, font.Normal).Layout),
		vspace(2),
		layout.Rigid(label(th, 13, "IP 주소나 내부 주소로 넣어도 괜찮아요.", colSub, font.Normal).Layout),
		vspace(14),
		layout.Rigid(field(th, &f.address, f.prov.AddressHint())),
	}
	for _, o := range f.prov.Options() {
		if o.Advanced {
			continue
		}
		children = append(children, vspace(10), layout.Rigid(f.optionField(o)))
	}
	if len(advanced) > 0 {
		arrow := "▸"
		if f.showAdv {
			arrow = "▾"
		}
		children = append(children, vspace(8), layout.Rigid(func(gtx C) D {
			b := smallButton(th, &f.adv, "고급 설정 "+arrow, colSub)
			b.Background = colCard
			b.Inset.Left = 2
			return b.Layout(gtx)
		}))
		if f.showAdv {
			for _, o := range advanced {
				children = append(children, vspace(6), layout.Rigid(f.optionField(o)))
			}
		}
	}
	children = append(children, vspace(12))

	switch ph {
	case checking:
		children = append(children, layout.Rigid(f.status("서버를 확인하는 중…", "")))
	case signingIn:
		host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://")
		children = append(children, layout.Rigid(f.status(waiting, host+" · "+srv.Product)))
	default:
		children = append(children, layout.Rigid(func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return primaryButton(th, &f.login, button).Layout(gtx)
		}))
	}

	if uerr != nil {
		children = append(children, vspace(12), layout.Rigid(label(th, 13, uerr.Msg, colDanger, font.Medium).Layout))
		if uerr.Hint != "" {
			children = append(children, vspace(3), layout.Rigid(label(th, 12, uerr.Hint, colSub, font.Normal).Layout))
		}
		if uerr.Err != nil {
			children = append(children, vspace(3), layout.Rigid(func(gtx C) D {
				l := label(th, 11, uerr.Err.Error(), colFaint, font.Normal)
				l.MaxLines = 2
				return l.Layout(gtx)
			}))
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

// passwordLogin reports whether the provider signs in with a password typed
// here rather than in the browser.
func passwordLogin(p provider.Provider) bool {
	for _, o := range p.Options() {
		if o.Secret && !o.Advanced {
			return true
		}
	}
	return false
}

// prefill opens the address step of p with the address and options filled
// in, to sign in again where only the person can (a changed password).
func (f *addFlow) prefill(p provider.Provider, address string, values map[string]string) {
	f.reset()
	f.step, f.prov = 1, p
	f.address.SetText(address)
	for k, v := range values {
		f.optEditor(p.Name() + "/" + k).SetText(v)
	}
}

func (f *addFlow) optionField(o provider.Option) layout.Widget {
	th := f.u.th
	ed := f.optEditor(f.prov.Name() + "/" + o.Key)
	if o.Secret {
		ed.Mask = '•'
	}
	return func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(label(th, 12, o.Label, colSub, font.Medium).Layout),
			vspace(4),
			layout.Rigid(field(th, ed, o.Hint)),
		)
	}
}

// status shows progress with a cancel button.
func (f *addFlow) status(title, sub string) layout.Widget {
	th := f.u.th
	return func(gtx C) D {
		return surface(gtx, colAccentDim, 12, func(gtx C) D {
			return layout.Inset{Top: 12, Bottom: 12, Left: 14, Right: 10}.Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx C) D {
						return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
							layout.Rigid(label(th, 14, title, colAccent, font.SemiBold).Layout),
							layout.Rigid(func(gtx C) D {
								if sub == "" {
									return D{}
								}
								return layout.Inset{Top: unit.Dp(3)}.Layout(gtx, label(th, 12, sub, colSub, font.Normal).Layout)
							}),
						)
					}),
					layout.Rigid(func(gtx C) D {
						b := smallButton(th, &f.stop, "취소", colSub)
						b.Background = colCard
						return b.Layout(gtx)
					}),
				)
			})
		})
	}
}
