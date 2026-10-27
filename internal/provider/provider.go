// Package provider defines the contract every storage service implements.
//
// A provider knows how to authenticate a user against one kind of server,
// list the drives (spaces, buckets, shares) that user can reach, and describe
// each drive as an rclone backend. Everything else — secret storage, token
// refresh locking, mounting — is shared and lives outside the provider.
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Account is the non-secret description of a signed-in user on a server.
// It is persisted as JSON in the user config directory.
type Account struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Server   string            `json:"server"`
	User     string            `json:"user"`
	Display  string            `json:"display,omitempty"`
	Extra    map[string]string `json:"extra,omitempty"` // provider-specific settings
}

// Secret holds credentials. Providers fill whichever fields they need;
// the store keeps each non-empty field in the OS keychain.
type Secret struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
	Password     string
}

// Valid reports whether the access token can still be used for a while.
func (s Secret) Valid() bool {
	return s.AccessToken != "" && (s.Expiry.IsZero() || time.Until(s.Expiry) > time.Minute)
}

// DriveKind classifies drives so callers can pick sensible defaults.
type DriveKind string

const (
	KindPersonal DriveKind = "personal"
	KindProject  DriveKind = "project"
	KindShares   DriveKind = "shares"
	KindOther    DriveKind = "other"
)

// Drive is one mountable root exposed by an account.
type Drive struct {
	ID   string    `json:"id"`
	Name string    `json:"name"`
	Kind DriveKind `json:"kind"`
	// Location is provider-specific, for example the drive's WebDAV URL.
	Location string `json:"location"`
	// Quota is nil when the server reports none for this drive.
	Quota *Quota `json:"quota,omitempty"`
}

// Quota is a drive's space as the server reports it, in bytes.
type Quota struct {
	Used  int64 `json:"used"`
	Total int64 `json:"total"` // 0 when the drive has no limit
}

// Unlimited reports whether the server sets no size limit.
func (q Quota) Unlimited() bool { return q.Total <= 0 }

// Free is what is left under the limit; 0 for unlimited drives.
func (q Quota) Free() int64 {
	if q.Unlimited() || q.Used >= q.Total {
		return 0
	}
	return q.Total - q.Used
}

// Remote describes how rclone reaches a drive.
type Remote struct {
	Backend string            // rclone backend name, e.g. "webdav"
	Root    string            // path inside the backend
	Options map[string]string // rclone backend options
}

// Env carries what a provider needs from the host program.
type Env struct {
	// OpenBrowser shows a URL to the user for interactive sign-in.
	OpenBrowser func(url string) error
	// TokenCommand, when run, prints a fresh access token for the account.
	// Providers using bearer tokens hand it to rclone so it can refresh
	// on its own during long mounts.
	TokenCommand func(acct Account) []string
}

// Server is a resolved server address.
type Server struct {
	URL     string // canonical address the server advertises for itself
	Product string // e.g. "OpenCloud 7.2.4", for confirmation in the UI
}

// Option is an extra, provider-specific input such as an OAuth client ID.
type Option struct {
	Key      string
	Label    string
	Hint     string
	Advanced bool // hidden behind "advanced settings" unless needed
	Secret   bool // a password: masked while typed, kept in the keychain by the provider
}

// LoginOptions are the user's inputs for signing in.
type LoginOptions struct {
	Server Server
	Values map[string]string // by Option.Key
}

// Provider is implemented once per kind of server.
type Provider interface {
	// Name is the stable identifier stored in accounts, e.g. "opencloud".
	Name() string
	// Title is shown to people, e.g. "OpenCloud".
	Title() string
	// Description says in one line which servers this covers.
	Description() string
	// AddressHint is the placeholder for the address field.
	AddressHint() string
	// Options lists extra inputs the user may give.
	Options() []Option
	// Resolve turns whatever the user typed (host, IP, port, scheme) into
	// the server's canonical address, checking it really is this kind of server.
	Resolve(ctx context.Context, input string) (Server, error)
	// Login signs the user in interactively and returns the new account.
	Login(ctx context.Context, env Env, opt LoginOptions) (Account, Secret, error)
	// Refresh returns a secret with a usable access token.
	Refresh(ctx context.Context, acct Account, sec Secret) (Secret, error)
	// Logout revokes server-side credentials where supported.
	Logout(ctx context.Context, acct Account, sec Secret) error
	// Drives lists what the account can mount.
	Drives(ctx context.Context, acct Account, sec Secret) ([]Drive, error)
	// Remote turns a drive into an rclone backend definition. sec is the
	// current secret, for providers that hand rclone a password.
	Remote(env Env, acct Account, sec Secret, d Drive) (Remote, error)
}

// Instant is implemented by providers whose servers take resumable (tus)
// uploads, which instant mode needs.
type Instant interface {
	// InstantSupported asks the server whether it takes tus uploads.
	InstantSupported(ctx context.Context, acct Account, sec Secret) bool
	// InstantFolder is the WebDAV URL uploads into d start from, or ""
	// when the drive can't take them.
	InstantFolder(d Drive) string
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// Register makes a provider available by name. It is meant to be called
// from a provider package's init function.
func Register(p Provider) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := providers[p.Name()]; dup {
		panic("provider: Register called twice for " + p.Name())
	}
	providers[p.Name()] = p
}

// Get returns the provider registered under name.
func Get(name string) (Provider, error) {
	mu.RLock()
	defer mu.RUnlock()
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q", name)
	}
	return p, nil
}

// Names lists registered providers in sorted order.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(providers))
	for n := range providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
