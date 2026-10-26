package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

type fakeProvider struct {
	provider.Provider
	refreshes int
}

func (f *fakeProvider) Refresh(_ context.Context, _ provider.Account, sec provider.Secret) (provider.Secret, error) {
	f.refreshes++
	if sec.RefreshToken != "r1" {
		return provider.Secret{}, errors.New("stale refresh token")
	}
	return provider.Secret{AccessToken: "a2", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
}

func newStore(t *testing.T) *Store {
	keyring.MockInit()
	d := t.TempDir()
	return &Store{ConfigDir: d + "/cfg", CacheDir: d + "/cache"}
}

func TestSaveSelectRemove(t *testing.T) {
	s := newStore(t)
	if _, err := s.Account(""); err == nil {
		t.Fatal("empty store returned an account")
	}
	a, err := s.Save(provider.Account{Provider: "opencloud", Server: "https://Drive.Example.com", User: "alice"}, provider.Secret{RefreshToken: "r"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "opencloud-drive.example.com-alice" {
		t.Fatalf("id = %q", a.ID)
	}
	if got, err := s.Account(""); err != nil || got.ID != a.ID {
		t.Fatalf("single account not selected: %v", err)
	}
	s.Save(provider.Account{Provider: "opencloud", Server: "https://drive.example.com", User: "bob"}, provider.Secret{})
	if _, err := s.Account(""); err == nil {
		t.Fatal("ambiguous selection accepted")
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if sec, _ := s.Secret(a.ID); sec.RefreshToken != "" {
		t.Fatal("secret survived removal")
	}
}

func TestFreshSecretRefreshesAndRotates(t *testing.T) {
	s := newStore(t)
	exp := time.Now().Add(-time.Minute)
	a, _ := s.Save(provider.Account{Provider: "x", Server: "https://s", User: "u"},
		provider.Secret{AccessToken: "a1", RefreshToken: "r1", Expiry: exp})

	p := &fakeProvider{}
	sec, err := s.FreshSecret(context.Background(), a, p)
	if err != nil || sec.AccessToken != "a2" {
		t.Fatalf("fresh = %+v, %v", sec, err)
	}
	// The rotated refresh token must be persisted and the new token reused.
	sec, err = s.FreshSecret(context.Background(), a, p)
	if err != nil || sec.AccessToken != "a2" || sec.RefreshToken != "r2" || p.refreshes != 1 {
		t.Fatalf("second = %+v, %v, refreshes=%d", sec, err, p.refreshes)
	}
	if time.Until(sec.Expiry) < 50*time.Minute {
		t.Fatalf("expiry not stored: %v", sec.Expiry)
	}
}
