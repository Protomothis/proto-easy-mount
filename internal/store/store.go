// Package store persists accounts as JSON and their secrets in the OS keychain
// (Windows Credential Manager, macOS Keychain, Secret Service on Linux).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

const (
	AppName        = "proto-easy-mount"
	keyringService = AppName
)

// Store manages accounts under one config directory.
type Store struct {
	ConfigDir string
	CacheDir  string
}

// Default uses the per-user config and cache directories of the OS.
func Default() (*Store, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Store{ConfigDir: filepath.Join(cfg, AppName), CacheDir: filepath.Join(cache, AppName)}, nil
}

func (s *Store) accountsFile() string { return filepath.Join(s.ConfigDir, "accounts.json") }

// LogPath is where the desktop app writes its log (rotated, three backups).
func LogPath(s *Store) string { return filepath.Join(s.CacheDir, "app.log") }

// Accounts returns every saved account.
func (s *Store) Accounts() ([]provider.Account, error) {
	b, err := os.ReadFile(s.accountsFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var accts []provider.Account
	if err := json.Unmarshal(b, &accts); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.accountsFile(), err)
	}
	return accts, nil
}

// Account finds an account by ID. An empty ID selects the only account.
func (s *Store) Account(id string) (provider.Account, error) {
	accts, err := s.Accounts()
	if err != nil {
		return provider.Account{}, err
	}
	if id == "" {
		switch len(accts) {
		case 0:
			return provider.Account{}, errors.New("no account; run login first")
		case 1:
			return accts[0], nil
		default:
			return provider.Account{}, errors.New("several accounts saved; choose one with --account")
		}
	}
	for _, a := range accts {
		if a.ID == id {
			return a, nil
		}
	}
	return provider.Account{}, fmt.Errorf("no account %q", id)
}

// Save adds or replaces an account and its secret. The account ID is
// derived from provider, server and user when empty.
func (s *Store) Save(acct provider.Account, sec provider.Secret) (provider.Account, error) {
	if acct.ID == "" {
		acct.ID = AccountID(acct)
	}
	accts, err := s.Accounts()
	if err != nil {
		return acct, err
	}
	replaced := false
	for i := range accts {
		if accts[i].ID == acct.ID {
			accts[i] = acct
			replaced = true
		}
	}
	if !replaced {
		accts = append(accts, acct)
	}
	if err := s.putSecret(acct.ID, sec); err != nil {
		return acct, err
	}
	return acct, s.writeAccounts(accts)
}

// Remove deletes an account and its secret.
func (s *Store) Remove(id string) error {
	accts, err := s.Accounts()
	if err != nil {
		return err
	}
	kept := accts[:0]
	for _, a := range accts {
		if a.ID != id {
			kept = append(kept, a)
		}
	}
	s.deleteSecret(id)
	return s.writeAccounts(kept)
}

func (s *Store) writeAccounts(accts []provider.Account) error {
	if err := os.MkdirAll(s.ConfigDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(accts, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.accountsFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.accountsFile())
}

// Secret loads the stored secret without refreshing it.
func (s *Store) Secret(id string) (provider.Secret, error) {
	var sec provider.Secret
	var err error
	if sec.RefreshToken, err = get(id, "refresh"); err != nil {
		return sec, err
	}
	if sec.Password, err = get(id, "password"); err != nil {
		return sec, err
	}
	access, err := get(id, "access")
	if err != nil {
		return sec, err
	}
	// access is stored as "<unix expiry>|<token>" so expiry travels with it
	if exp, tok, ok := strings.Cut(access, "|"); ok {
		var unix int64
		fmt.Sscan(exp, &unix)
		if unix > 0 {
			sec.Expiry = time.Unix(unix, 0)
		}
		sec.AccessToken = tok
	}
	return sec, nil
}

// FreshSecret returns a secret with a usable access token, refreshing it
// through the provider when needed. A file lock serialises refreshes across
// processes, since rclone may run the token command concurrently and
// issuers that rotate refresh tokens reject reuse of an old one.
func (s *Store) FreshSecret(ctx context.Context, acct provider.Account, p provider.Provider) (provider.Secret, error) {
	sec, err := s.Secret(acct.ID)
	if err != nil {
		return sec, err
	}
	if sec.Valid() || (sec.RefreshToken == "" && sec.Password != "") {
		return sec, nil
	}
	if err := os.MkdirAll(s.CacheDir, 0o700); err != nil {
		return sec, err
	}
	lock := flock.New(filepath.Join(s.CacheDir, safeName(acct.ID)+".lock"))
	if ok, err := lock.TryLockContext(ctx, 100*time.Millisecond); err != nil || !ok {
		return sec, fmt.Errorf("lock token: %w", err)
	}
	defer lock.Unlock()

	// another process may have refreshed while we waited
	if sec, err = s.Secret(acct.ID); err != nil || sec.Valid() {
		return sec, err
	}
	fresh, err := p.Refresh(ctx, acct, sec)
	if err != nil {
		return sec, err
	}
	return fresh, s.putSecret(acct.ID, fresh)
}

func (s *Store) putSecret(id string, sec provider.Secret) error {
	access := ""
	if sec.AccessToken != "" {
		var unix int64
		if !sec.Expiry.IsZero() {
			unix = sec.Expiry.Unix()
		}
		access = fmt.Sprintf("%d|%s", unix, sec.AccessToken)
	}
	// Each field gets its own entry: Windows caps a credential blob at 2560 bytes.
	for field, v := range map[string]string{"access": access, "refresh": sec.RefreshToken, "password": sec.Password} {
		if err := put(id, field, v); err != nil {
			return fmt.Errorf("save %s to keychain: %w", field, err)
		}
	}
	return nil
}

func (s *Store) deleteSecret(id string) {
	for _, f := range []string{"access", "refresh", "password"} {
		keyring.Delete(keyringService, id+":"+f)
	}
}

func get(id, field string) (string, error) {
	v, err := keyring.Get(keyringService, id+":"+field)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func put(id, field, v string) error {
	if v == "" {
		if err := keyring.Delete(keyringService, id+":"+field); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return err
		}
		return nil
	}
	return keyring.Set(keyringService, id+":"+field, v)
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeName(s string) string { return unsafeChars.ReplaceAllString(s, "_") }

// AccountID builds a readable, filesystem-safe identifier such as
// "opencloud-cloud.example.com-alice".
func AccountID(a provider.Account) string {
	host := strings.TrimPrefix(strings.TrimPrefix(a.Server, "https://"), "http://")
	return strings.ToLower(safeName(a.Provider + "-" + host + "-" + a.User))
}
