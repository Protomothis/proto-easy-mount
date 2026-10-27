package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/store"
)

// flaky is a provider whose drive listing and token refresh fail on demand.
type flaky struct {
	mu         sync.Mutex
	offline    bool // drive listing can't reach the server
	rejected   bool // the issuer refuses the refresh token
	refreshed  int
	listed     int
	signedInAt int
}

func (f *flaky) set(fn func(*flaky)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

func (*flaky) Name() string               { return "flaky" }
func (*flaky) Title() string              { return "Flaky" }
func (*flaky) Description() string        { return "" }
func (*flaky) AddressHint() string        { return "" }
func (*flaky) Options() []provider.Option { return nil }
func (*flaky) Resolve(_ context.Context, in string) (provider.Server, error) {
	return provider.Server{URL: in}, nil
}
func (f *flaky) Login(_ context.Context, _ provider.Env, o provider.LoginOptions) (provider.Account, provider.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signedInAt++
	f.rejected = false
	return provider.Account{Provider: "flaky", Server: o.Server.URL, User: "u"},
		provider.Secret{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
}
func (f *flaky) Refresh(_ context.Context, _ provider.Account, s provider.Secret) (provider.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed++
	if f.rejected {
		return s, fmt.Errorf("refresh: %w", provider.ErrSignIn)
	}
	return provider.Secret{AccessToken: "fresh", RefreshToken: s.RefreshToken, Expiry: time.Now().Add(time.Hour)}, nil
}
func (*flaky) Logout(context.Context, provider.Account, provider.Secret) error { return nil }
func (f *flaky) Drives(context.Context, provider.Account, provider.Secret) ([]provider.Drive, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listed++
	if f.offline {
		return nil, errors.New("dial tcp: no route to host")
	}
	return []provider.Drive{{ID: "me", Name: "me", Kind: provider.KindPersonal}}, nil
}
func (*flaky) Remote(provider.Env, provider.Account, provider.Secret, provider.Drive) (provider.Remote, error) {
	return provider.Remote{}, nil
}

var theFlaky = &flaky{}

func init() { provider.Register(theFlaky) }

func flakyService(t *testing.T, expired bool) (*Service, string) {
	keyring.MockInit()
	*theFlaky = flaky{}
	d := t.TempDir()
	st := &store.Store{ConfigDir: d + "/cfg", CacheDir: d + "/cache"}
	exp := time.Now().Add(time.Hour)
	if expired {
		exp = time.Now().Add(-time.Minute)
	}
	acct, err := st.Save(provider.Account{Provider: "flaky", Server: "https://s", User: "u"},
		provider.Secret{AccessToken: "a", RefreshToken: "r", Expiry: exp})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, Env(func(string) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	return s, acct.ID
}

func view(s *Service, id string) AccountView {
	for _, a := range s.Snapshot() {
		if a.ID == id {
			return a
		}
	}
	return AccountView{}
}

// Started before the network is up: the account shows a retryable error,
// is retried, and recovers on its own once the server answers.
func TestOfflineStartRecovers(t *testing.T) {
	s, id := flakyService(t, false)
	theFlaky.set(func(f *flaky) { f.offline = true })
	s.RefreshAll(context.Background())

	a := view(s, id)
	if a.Err == "" || a.NeedsSignIn {
		t.Fatalf("offline: err=%q needsSignIn=%v", a.Err, a.NeedsSignIn)
	}
	if got := s.failing(); len(got) != 1 || got[0] != id {
		t.Fatalf("failing = %v", got)
	}

	theFlaky.set(func(f *flaky) { f.offline = false })
	s.refreshQuiet(context.Background(), id)
	a = view(s, id)
	if a.Err != "" || len(a.Drives) != 1 {
		t.Fatalf("after recovery: err=%q drives=%d", a.Err, len(a.Drives))
	}
	if len(s.failing()) != 0 {
		t.Fatal("still retried after recovering")
	}
}

// A refused refresh token is not retried; signing in again fixes it and
// keeps the account.
func TestExpiredSessionNeedsSignIn(t *testing.T) {
	s, id := flakyService(t, true)
	theFlaky.set(func(f *flaky) { f.rejected = true })
	s.RefreshAll(context.Background())

	a := view(s, id)
	if !a.NeedsSignIn || a.Err == "" {
		t.Fatalf("expired: err=%q needsSignIn=%v", a.Err, a.NeedsSignIn)
	}
	if len(s.failing()) != 0 {
		t.Fatal("an expired session is retried in a loop")
	}

	if err := s.SignInAgain(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	a = view(s, id)
	if a.NeedsSignIn || a.Err != "" || len(a.Drives) != 1 {
		t.Fatalf("after sign-in: err=%q needsSignIn=%v drives=%d", a.Err, a.NeedsSignIn, len(a.Drives))
	}
	if n := len(s.Snapshot()); n != 1 {
		t.Fatalf("%d accounts after signing in again, want 1", n)
	}
}
