// Package webaddr turns what people type as a server address into the URLs
// worth trying, for providers of HTTP servers.
package webaddr

import (
	"net"
	"net/url"
	"strings"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

// Candidates lists the base URLs worth trying for what the user typed, path
// kept, without a trailing slash. Without a scheme HTTPS comes first; plain
// HTTP is tried too for addresses that only make sense on a local network.
func Candidates(input string) ([]string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, provider.Explain("서버 주소를 입력해 주세요.", "", nil)
	}
	explicit := strings.Contains(s, "://")
	if !explicit {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, provider.Explain("주소 형식이 올바르지 않아요.", "예: cloud.example.com", err)
	}
	base := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	if explicit || !IsLocalHost(u.Hostname()) {
		return []string{base}, nil
	}
	return []string{base, "http://" + strings.TrimPrefix(base, "https://")}, nil
}

// IsLocalHost reports whether h only makes sense on a local network: an IP
// address, a single-label name or a typical home-network suffix.
func IsLocalHost(h string) bool {
	if net.ParseIP(h) != nil || h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home", ".internal", ".localdomain", ".home.arpa"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// Host is the server's host (and port) for display.
func Host(server string) string {
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		return u.Host
	}
	return server
}
