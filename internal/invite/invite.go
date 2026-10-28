// Package invite builds and reads connection links: a link copied on one PC
// opens the app on another with the service, server address and server-side
// settings filled in, so only signing in is left (#18).
//
//	proto-easy-mount://add?provider=opencloud&server=https%3A%2F%2Fdrive.example.com&client_id=1234
//
// A link names a server, never a person: no user name, password or token.
package invite

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

// Scheme is the URL scheme the installer registers for the app.
const Scheme = "proto-easy-mount"

const (
	action      = "add"
	keyProvider = "provider"
	keyServer   = "server"
)

// Link is what a connection link carries.
type Link struct {
	Provider string            // provider name, e.g. "opencloud"
	Server   string            // the account's server address
	Options  map[string]string // server-side login options, by Option.Key
}

// For makes the link for an account. Only the provider's advanced,
// non-secret options go in: they describe the server (a client ID), while
// the others (a user name) belong to one person.
func For(p provider.Provider, acct provider.Account) Link {
	l := Link{Provider: p.Name(), Server: acct.Server, Options: map[string]string{}}
	for _, o := range Shared(p) {
		if v := acct.Extra[o.Key]; v != "" {
			l.Options[o.Key] = v
		}
	}
	return l
}

// Shared lists the options of p a link may carry.
func Shared(p provider.Provider) []provider.Option {
	var opts []provider.Option
	for _, o := range p.Options() {
		if o.Advanced && !o.Secret && o.Key != keyProvider && o.Key != keyServer {
			opts = append(opts, o)
		}
	}
	return opts
}

// String renders the link.
func (l Link) String() string {
	q := url.Values{}
	q.Set(keyProvider, l.Provider)
	q.Set(keyServer, l.Server)
	keys := make([]string, 0, len(l.Options))
	for k := range l.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		q.Set(k, l.Options[k])
	}
	return Scheme + "://" + action + "?" + q.Encode()
}

// IsLink reports whether a command-line argument is a connection link, as
// Windows passes it when one is opened.
func IsLink(arg string) bool {
	return strings.HasPrefix(strings.ToLower(arg), Scheme+":")
}

// Parse reads a link. It checks the shape only; the caller checks the
// provider and keeps just the options that provider shares.
func Parse(raw string) (Link, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) {
		return Link{}, errors.New("not a " + Scheme + " link")
	}
	// "proto-easy-mount://add?…" puts the action in Host; some apps
	// rewrite it to "proto-easy-mount:add?…" (Opaque).
	if act := strings.Trim(u.Host+u.Opaque+u.Path, "/"); act != action {
		return Link{}, fmt.Errorf("unknown link action %q", act)
	}
	q := u.Query()
	l := Link{Provider: q.Get(keyProvider), Server: q.Get(keyServer), Options: map[string]string{}}
	if l.Provider == "" {
		return Link{}, errors.New("link has no provider")
	}
	s, err := url.Parse(l.Server)
	if err != nil || (s.Scheme != "https" && s.Scheme != "http") || s.Host == "" {
		return Link{}, fmt.Errorf("link has no usable server address: %q", l.Server)
	}
	for k, v := range q {
		if k != keyProvider && k != keyServer && len(v) > 0 && v[0] != "" {
			l.Options[k] = v[0]
		}
	}
	return l, nil
}

// Keep drops the options p does not share, so a crafted link cannot fill
// in a password or user name.
func (l Link) Keep(p provider.Provider) Link {
	out := Link{Provider: l.Provider, Server: l.Server, Options: map[string]string{}}
	for _, o := range Shared(p) {
		if v, ok := l.Options[o.Key]; ok {
			out.Options[o.Key] = v
		}
	}
	return out
}
