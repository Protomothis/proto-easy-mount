// Package core is the application layer shared by the command line and the
// desktop app: it owns accounts, drive lists and live mounts, and tells
// listeners when anything changes.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/store"

	// registered providers
	_ "github.com/protomothis/proto-easy-mount/internal/provider/opencloud"
)

// Env builds the provider environment. The token command re-runs this
// executable with "token --account <id>".
func Env(openBrowser func(string) error) provider.Env {
	return provider.Env{
		OpenBrowser: openBrowser,
		TokenCommand: func(acct provider.Account) []string {
			self, err := os.Executable()
			if err != nil {
				self = os.Args[0]
			}
			return []string{self, "token", "--account", acct.ID}
		},
	}
}

// Token returns a fresh access token for an account; it backs the hidden
// "token" command that rclone runs while a drive is mounted.
func Token(ctx context.Context, st *store.Store, accountID string) (string, error) {
	acct, err := st.Account(accountID)
	if err != nil {
		return "", err
	}
	p, err := provider.Get(acct.Provider)
	if err != nil {
		return "", err
	}
	sec, err := st.FreshSecret(ctx, acct, p)
	if err != nil {
		return "", err
	}
	if sec.AccessToken == "" {
		return "", errors.New("account has no access token")
	}
	return sec.AccessToken, nil
}

// State of a drive's mount.
type State int

const (
	Idle State = iota
	Connecting
	Mounted
	Failed
	// Suspended: switched off while the server was unreachable; it comes
	// back by itself once the server answers again.
	Suspended
)

// DriveView is a drive together with its mount state.
type DriveView struct {
	provider.Drive
	State     State
	Target    string // where it is mounted
	Preferred string // chosen drive letter or directory; empty means automatic
	Err       string
	Uploads   int  // files written locally and not yet on the server
	Mode      Mode // how writes reach the server
	InstantOK bool // instant mode is possible for this drive here
}

// AccountView is an account with its drives, for display.
type AccountView struct {
	provider.Account
	Title   string // provider title
	Drives  []DriveView
	Loading bool
	Err     string
	// NeedsSignIn means the server no longer accepts the session.
	NeedsSignIn bool
	// Quota is the account's space: its personal drive's quota, nil if unknown.
	Quota *provider.Quota
}

// Activity summarises what all mounts are doing right now.
type Activity struct {
	Up      int64 // upload speed, bytes per second
	Down    int64 // download speed, bytes per second
	Uploads int   // files waiting to reach the server
	// Files are the uploads in flight right now, by name.
	Files []mount.Transfer
}

func (a Activity) equal(b Activity) bool {
	return a.Up == b.Up && a.Down == b.Down && a.Uploads == b.Uploads && slices.Equal(a.Files, b.Files)
}

type accountState struct {
	acct    provider.Account
	prov    provider.Provider
	drives  []provider.Drive
	instant bool // the server takes tus uploads (instant mode)
	loading bool
	err     string
	signIn  bool   // the session is gone; err asks to sign in again
	restore bool   // remount the drives left connected once the list loads
	health  health // network watch
}

type mountKey struct{ acct, drive string }

type mountState struct {
	state  State
	target string
	err    string
	m      *mount.Mounted
	// stopping is set when the user asked to unmount, so the waiter
	// does not report the end of the mount as a failure.
	stopping bool
	// suspended is set when the network watch switched the drive off.
	suspended bool
	uploads   int
}

// Service is safe for concurrent use.
type Service struct {
	store *store.Store
	env   provider.Env
	prefs *Prefs

	pickMu    sync.Mutex // serialises automatic drive letter picks
	mu        sync.Mutex
	order     []string // account IDs in saved order
	accounts  map[string]*accountState
	mounts    map[mountKey]*mountState
	listeners []func()
	activity  Activity
	// Notify, when set, shows a system notification (network watch).
	Notify func(title, text string)
}

// New loads saved accounts. Drive lists are fetched by Refresh.
func New(st *store.Store, env provider.Env) (*Service, error) {
	prefs, err := loadPrefs(st.ConfigDir)
	if err != nil {
		return nil, err
	}
	if err := mount.SetCacheDir(st.CacheDir); err != nil {
		return nil, err
	}
	s := &Service{store: st, env: env, prefs: prefs, accounts: map[string]*accountState{}, mounts: map[mountKey]*mountState{}}
	// Instant-mode files still in flight when the app last stopped were
	// either finished or handed to the write cache; the copies are stale.
	os.RemoveAll(s.shadowDir())
	accts, err := st.Accounts()
	if err != nil {
		return nil, err
	}
	for _, a := range accts {
		p, err := provider.Get(a.Provider)
		if err != nil {
			continue // provider removed from this build
		}
		s.order = append(s.order, a.ID)
		s.accounts[a.ID] = &accountState{acct: a, prov: p}
	}
	return s, nil
}

// Subscribe registers fn to be called, from any goroutine, after changes.
func (s *Service) Subscribe(fn func()) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

func (s *Service) notify() {
	s.mu.Lock()
	ls := append([]func(){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range ls {
		fn()
	}
}

// Snapshot returns the current state for display.
func (s *Service) Snapshot() []AccountView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AccountView, 0, len(s.order))
	for _, id := range s.order {
		as := s.accounts[id]
		v := AccountView{Account: as.acct, Title: as.prov.Title(), Loading: as.loading, Err: as.err, NeedsSignIn: as.signIn}
		for _, d := range as.drives {
			dv := DriveView{Drive: d, Preferred: s.prefs.target(id, d.ID), Mode: s.prefs.mode(id, d.ID), InstantOK: s.instantFolder(as, d) != ""}
			if ms := s.mounts[mountKey{id, d.ID}]; ms != nil {
				dv.State, dv.Target, dv.Err, dv.Uploads = ms.state, ms.target, ms.err, ms.uploads
			}
			if d.Kind == provider.KindPersonal && d.Quota != nil {
				q := *d.Quota
				v.Quota = &q
			}
			v.Drives = append(v.Drives, dv)
		}
		out = append(out, v)
	}
	return out
}

// MountedCount is the number of drives currently mounted.
func (s *Service) MountedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ms := range s.mounts {
		if ms.state == Mounted {
			n++
		}
	}
	return n
}

// Providers lists the services users can add.
func Providers() []provider.Provider {
	var ps []provider.Provider
	for _, n := range provider.Names() {
		p, _ := provider.Get(n)
		ps = append(ps, p)
	}
	return ps
}

// Resolve checks what the user typed and finds the server's canonical address.
func (s *Service) Resolve(ctx context.Context, providerName, input string) (provider.Server, error) {
	p, err := provider.Get(providerName)
	if err != nil {
		return provider.Server{}, err
	}
	return p.Resolve(ctx, input)
}

// Login signs in interactively, saves the account and loads its drives.
func (s *Service) Login(ctx context.Context, providerName string, server provider.Server, values map[string]string) (provider.Account, error) {
	p, err := provider.Get(providerName)
	if err != nil {
		return provider.Account{}, err
	}
	acct, sec, err := p.Login(ctx, s.env, provider.LoginOptions{Server: server, Values: values})
	if err != nil {
		slog.Warn(fmt.Sprintf("로그인 실패 %s: %v", server.URL, err))
		return provider.Account{}, err
	}
	acct, err = s.store.Save(acct, sec)
	if err != nil {
		slog.Error(fmt.Sprintf("계정 저장 실패 %s: %v", acct.ID, err))
		return acct, err
	}
	slog.Info(fmt.Sprintf("로그인 %s (%s) %s", acct.ID, acct.Display, server.Product))
	s.mu.Lock()
	next := &accountState{acct: acct, prov: p}
	if prev, ok := s.accounts[acct.ID]; ok {
		next.restore = prev.restore // signing in again
	} else {
		s.order = append(s.order, acct.ID)
	}
	s.accounts[acct.ID] = next
	s.mu.Unlock()
	s.notify()
	// A failed drive listing shows on the account; the sign-in itself worked.
	s.Refresh(ctx, acct.ID)
	return acct, nil
}

// Logout unmounts the account's drives, revokes the session and forgets it.
func (s *Service) Logout(ctx context.Context, id string) error {
	s.mu.Lock()
	as := s.accounts[id]
	s.mu.Unlock()
	if as == nil {
		return fmt.Errorf("no account %q", id)
	}
	for _, d := range as.drives {
		s.unmount(id, d.ID, false)
	}
	if sec, err := s.store.Secret(id); err == nil {
		as.prov.Logout(ctx, as.acct, sec) // best effort
	}
	if err := s.store.Remove(id); err != nil {
		return err
	}
	s.prefs.forget(id)
	s.mu.Lock()
	delete(s.accounts, id)
	for i, x := range s.order {
		if x == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.mu.Unlock()
	s.notify()
	return nil
}

// Refresh reloads the drive list of an account.
func (s *Service) Refresh(ctx context.Context, id string) error {
	s.mu.Lock()
	as := s.accounts[id]
	if as == nil {
		s.mu.Unlock()
		return fmt.Errorf("no account %q", id)
	}
	as.loading, as.err = true, ""
	s.mu.Unlock()
	s.notify()

	drives, instant, err := s.fetchDrives(ctx, as)

	s.mu.Lock()
	as.loading = false
	if err != nil {
		as.err, as.signIn = err.Error(), provider.NeedsSignIn(err)
		slog.Warn(fmt.Sprintf("공간 목록 실패 %s: %v", id, err))
	}
	s.mu.Unlock()
	if err == nil {
		s.loaded(as, drives, instant)
	}
	s.notify()
	return err
}

// loaded stores a fresh drive list, clears an earlier error and, the first
// time the list loads after start, remounts the drives left connected.
func (s *Service) loaded(as *accountState, drives []provider.Drive, instant bool) {
	s.mu.Lock()
	as.drives, as.instant = drives, instant
	as.err, as.signIn = "", false
	restore := as.restore
	as.restore = false
	s.mu.Unlock()
	if restore {
		for _, d := range s.prefs.remembered(as.acct.ID) {
			s.Mount(as.acct.ID, d)
		}
	}
}

// sessionError words a failure to get a token: an expired session needs
// the person, anything else (no network yet, server down) is retried.
func sessionError(err error) error {
	if provider.NeedsSignIn(err) {
		return provider.Explain("로그인이 만료됐어요.", "다시 로그인해 주세요.", err)
	}
	return provider.Explain("서버에 연결하지 못했어요.", "연결되면 자동으로 다시 시도해요.", err)
}

// SignInAgain repeats the sign-in of an account whose session expired,
// keeping its drives and settings. Providers keep their login options in
// Account.Extra under the option keys, so they are offered again.
func (s *Service) SignInAgain(ctx context.Context, id string) error {
	s.mu.Lock()
	as := s.accounts[id]
	s.mu.Unlock()
	if as == nil {
		return fmt.Errorf("no account %q", id)
	}
	server, err := as.prov.Resolve(ctx, as.acct.Server)
	if err != nil {
		return err
	}
	s.mu.Lock()
	as.restore = true // drives that dropped with the session come back
	s.mu.Unlock()
	_, err = s.Login(ctx, as.prov.Name(), server, as.acct.Extra)
	return err
}

// fetchDrives lists the account's drives and whether its server can take
// instant-mode uploads.
func (s *Service) fetchDrives(ctx context.Context, as *accountState) ([]provider.Drive, bool, error) {
	sec, err := s.store.FreshSecret(ctx, as.acct, as.prov)
	if err != nil {
		return nil, false, sessionError(err)
	}
	drives, err := as.prov.Drives(ctx, as.acct, sec)
	if err != nil {
		return nil, false, err
	}
	instant := false
	if ip, ok := as.prov.(provider.Instant); ok && mount.SupportsInstant() {
		instant = ip.InstantSupported(ctx, as.acct, sec)
	}
	return drives, instant, nil
}

// instantFolder is where instant uploads into d start, or "" when instant
// mode isn't possible for it. Call with s.mu held.
func (s *Service) instantFolder(as *accountState, d provider.Drive) string {
	ip, ok := as.prov.(provider.Instant)
	if !ok || !as.instant {
		return ""
	}
	return ip.InstantFolder(d)
}

// Mode is how a drive's writes reach the server.
type Mode string

const (
	// Stable: copies finish at local speed, uploads follow in the background.
	Stable Mode = ""
	// Instant: a copy finishes once the server has the file.
	Instant Mode = "instant"
)

// SetMode chooses how a drive uploads; it applies the next time the drive
// is connected.
func (s *Service) SetMode(acctID, driveID string, m Mode) error {
	s.mu.Lock()
	ms := s.mounts[mountKey{acctID, driveID}]
	busy := ms != nil && (ms.state == Connecting || ms.state == Mounted)
	s.mu.Unlock()
	if busy {
		return errors.New("연결을 끈 뒤에 바꿀 수 있어요")
	}
	err := s.prefs.setMode(acctID, driveID, m)
	s.notify()
	return err
}

// RefreshAll reloads every account and remounts what the user left
// connected last time.
func (s *Service) RefreshAll(ctx context.Context) {
	s.mu.Lock()
	ids := append([]string{}, s.order...)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		s.mu.Lock()
		s.accounts[id].restore = true // if this fails, Watch retries and restores
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Refresh(ctx, id)
		}()
	}
	wg.Wait()
}

// Mount starts mounting a drive in the background.
func (s *Service) Mount(acctID, driveID string) {
	k := mountKey{acctID, driveID}
	s.mu.Lock()
	as := s.accounts[acctID]
	if as == nil {
		s.mu.Unlock()
		return
	}
	var drive *provider.Drive
	for i := range as.drives {
		if as.drives[i].ID == driveID {
			drive = &as.drives[i]
		}
	}
	if ms := s.mounts[k]; drive == nil || (ms != nil && (ms.state == Connecting || ms.state == Mounted)) {
		s.mu.Unlock()
		return
	}
	d := *drive
	ms := &mountState{state: Connecting}
	s.mounts[k] = ms
	s.mu.Unlock()
	s.notify()

	go func() {
		m, err := s.start(as, d)
		if err == nil && s.prefs.target(acctID, driveID) == "" {
			// keep an automatically picked mount point, so the drive stays where the user found it
			s.prefs.setTarget(acctID, driveID, m.Target)
		}
		s.mu.Lock()
		cancelled := ms.stopping
		switch {
		case err != nil:
			ms.state, ms.err = Failed, err.Error()
			slog.Error(fmt.Sprintf("연결 실패 %s/%s: %v", acctID, d.Name, err))
		case cancelled:
			// the user switched it off while it was connecting
			ms.state = Idle
		default:
			ms.state, ms.target, ms.m = Mounted, m.Target, m
			slog.Info(fmt.Sprintf("연결됨 %s/%s → %s", acctID, d.Name, m.Target))
		}
		s.mu.Unlock()
		if err == nil && cancelled {
			m.Unmount()
			s.notify()
			return
		}
		if err == nil {
			s.prefs.setMounted(acctID, driveID, true)
		}
		s.notify()
		if err != nil {
			return
		}

		werr := m.Wait()
		s.mu.Lock()
		if ms.suspended {
			ms.state, ms.m = Suspended, nil // keeps target: it comes back there
		} else if ms.stopping || werr == nil {
			ms.state, ms.target, ms.m = Idle, "", nil
		} else {
			ms.state, ms.err, ms.m = Failed, werr.Error(), nil
			slog.Error(fmt.Sprintf("연결 끊김 %s/%s: %v", acctID, d.Name, werr))
		}
		s.mu.Unlock()
		s.notify()
	}()
}

func (s *Service) start(as *accountState, d provider.Drive) (*mount.Mounted, error) {
	ctx := context.Background()
	// Fail early with a clear message if the session is gone.
	if _, err := s.store.FreshSecret(ctx, as.acct, as.prov); err != nil {
		return nil, sessionError(err)
	}
	r, err := as.prov.Remote(s.env, as.acct, d)
	if err != nil {
		return nil, err
	}
	target, err := s.pickTarget(as.acct.ID, d.ID)
	if err != nil {
		return nil, err
	}
	req := mount.Request{
		Name:   as.acct.ID + "-" + d.ID,
		Remote: r,
		Target: target,
		Label:  Label(as.prov, d),
	}
	if q := d.Quota; q != nil && !q.Unlimited() {
		req.TotalSize = q.Total
	}
	s.mu.Lock()
	folder := s.instantFolder(as, d)
	s.mu.Unlock()
	if s.prefs.mode(as.acct.ID, d.ID) == Instant && folder != "" {
		acct, prov := as.acct, as.prov
		req.Instant = &mount.Instant{
			Folder:    folder,
			ShadowDir: s.shadowDir(),
			Token: func(ctx context.Context) (string, error) {
				sec, err := s.store.FreshSecret(ctx, acct, prov)
				return sec.AccessToken, err
			},
		}
		slog.Info(fmt.Sprintf("즉시 반영형으로 연결 %s/%s", acct.ID, d.Name))
	}
	return mount.Start(ctx, req)
}

// pickTarget returns the drive's chosen letter if it is still free, or the
// highest free letter no other drive has claimed.
func (s *Service) pickTarget(acctID, driveID string) (string, error) {
	want := s.prefs.target(acctID, driveID)
	if !mount.UsesDriveLetters() {
		return want, nil
	}
	if want != "" {
		if !mount.DriveLetterFree(want) {
			return "", fmt.Errorf("%s 드라이브가 이미 사용 중입니다. 다른 문자를 골라 주세요", want)
		}
		return want, nil
	}
	// Claim the letter right away: drives restored together pick concurrently.
	s.pickMu.Lock()
	defer s.pickMu.Unlock()
	free := s.AvailableTargets(acctID, driveID)
	if len(free) == 0 {
		return "", errors.New("남은 드라이브 문자가 없습니다")
	}
	letter := free[len(free)-1]
	return letter, s.prefs.setTarget(acctID, driveID, letter)
}

// AvailableTargets lists drive letters this drive may use: free on the
// system and not claimed by another drive. Empty where letters don't apply.
func (s *Service) AvailableTargets(acctID, driveID string) []string {
	var out []string
	for _, l := range mount.FreeDriveLetters() {
		a, d := s.prefs.targetOwner(l)
		if a == "" || (a == acctID && d == driveID) {
			out = append(out, l)
		}
	}
	return out
}

// SetTarget chooses where a drive mounts; empty means automatic.
// It applies the next time the drive is connected.
func (s *Service) SetTarget(acctID, driveID, target string) error {
	s.mu.Lock()
	ms := s.mounts[mountKey{acctID, driveID}]
	busy := ms != nil && (ms.state == Connecting || ms.state == Mounted)
	s.mu.Unlock()
	if busy {
		return errors.New("연결을 끈 뒤에 바꿀 수 있어요")
	}
	err := s.prefs.setTarget(acctID, driveID, target)
	s.notify()
	return err
}

// Unmount detaches a drive and forgets it for the next start.
func (s *Service) Unmount(acctID, driveID string) { s.unmount(acctID, driveID, true) }

func (s *Service) unmount(acctID, driveID string, forget bool) {
	k := mountKey{acctID, driveID}
	s.mu.Lock()
	ms := s.mounts[k]
	var m *mount.Mounted
	if ms != nil {
		if ms.state == Failed || ms.state == Suspended {
			ms.state, ms.err, ms.target = Idle, "", ""
		}
		ms.suspended = false // switched off by hand: stays off
		m = ms.m
		ms.stopping = true
	}
	s.mu.Unlock()
	if forget {
		s.prefs.setMounted(acctID, driveID, false)
	}
	if m != nil {
		slog.Info(fmt.Sprintf("연결 해제 %s/%s (%s)", acctID, driveID, m.Target))
		if err := m.Unmount(); err != nil {
			slog.Error(fmt.Sprintf("연결 해제 실패 %s/%s: %v", acctID, driveID, err))
		}
	}
	s.notify()
}

// Shutdown unmounts everything but remembers it for the next start.
func (s *Service) Shutdown() {
	s.mu.Lock()
	var keys []mountKey
	for k, ms := range s.mounts {
		if ms.m != nil {
			keys = append(keys, k)
		}
	}
	s.mu.Unlock()
	for _, k := range keys {
		s.unmount(k.acct, k.drive, false)
	}
}

// Label is the volume name shown in the file manager.
func Label(p provider.Provider, d provider.Drive) string {
	if d.Kind == provider.KindPersonal {
		return p.Title()
	}
	return p.Title() + " - " + d.Name
}

// shadowDir keeps instant-mode files while they upload.
func (s *Service) shadowDir() string { return filepath.Join(s.store.CacheDir, "stream") }
