// Package webdav connects any WebDAV server with a user name and password:
// NAS boxes (Synology, QNAP), Apache or nginx WebDAV, hosted services.
package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/provider/webaddr"
)

const (
	optUser     = "user"
	optPassword = "password"
)

type webdav struct{ http *http.Client }

func init() {
	provider.Register(&webdav{http: &http.Client{Timeout: 30 * time.Second}})
}

func (*webdav) Name() string  { return "webdav" }
func (*webdav) Title() string { return "WebDAV" }
func (*webdav) Description() string {
	return "WebDAV 주소 · NAS(시놀로지 · QNAP), 기타 서버"
}
func (*webdav) AddressHint() string { return "예: https://nas.example.com:5006/home" }

func (*webdav) Options() []provider.Option {
	return []provider.Option{
		{Key: optUser, Label: "사용자 이름"},
		{Key: optPassword, Label: "비밀번호", Secret: true},
	}
}

// Resolve checks that the address answers like a WebDAV server: a DAV
// header on OPTIONS, or asking for a login.
func (w *webdav) Resolve(ctx context.Context, input string) (provider.Server, error) {
	bases, err := webaddr.Candidates(input)
	if err != nil {
		return provider.Server{}, err
	}
	var lastErr error
	for _, base := range bases {
		req, err := http.NewRequestWithContext(ctx, http.MethodOptions, base+"/", nil)
		if err != nil {
			return provider.Server{}, err
		}
		resp, err := w.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		dav := resp.Header.Get("DAV") != ""
		if !dav && resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusMultiStatus {
			lastErr = fmt.Errorf("OPTIONS %s: HTTP %d without a DAV header", base, resp.StatusCode)
			continue
		}
		product := "WebDAV"
		if s := resp.Header.Get("Server"); s != "" {
			product += " · " + s
		}
		return provider.Server{URL: base, Product: product}, nil
	}
	return provider.Server{}, provider.Explain("이 주소에서 WebDAV 서버를 찾지 못했어요.",
		"WebDAV 주소를 경로까지 넣어 주세요(서비스 안내에 있는 주소).", lastErr)
}

// Login checks the user name and password against the server.
func (w *webdav) Login(ctx context.Context, _ provider.Env, opt provider.LoginOptions) (provider.Account, provider.Secret, error) {
	user, pass := strings.TrimSpace(opt.Values[optUser]), opt.Values[optPassword]
	if user == "" || pass == "" {
		return provider.Account{}, provider.Secret{}, provider.Explain("사용자 이름과 비밀번호를 넣어 주세요.", "", nil)
	}
	acct := provider.Account{Provider: w.Name(), Server: opt.Server.URL, User: user, Display: user}
	sec := provider.Secret{Password: pass}
	if _, err := w.quota(ctx, acct, sec); err != nil {
		if provider.NeedsSignIn(err) {
			return provider.Account{}, provider.Secret{}, provider.Explain("사용자 이름이나 비밀번호가 맞지 않아요.", "", nil)
		}
		return provider.Account{}, provider.Secret{}, provider.Explain("WebDAV 서버에 로그인하지 못했어요.", "", err)
	}
	return acct, sec, nil
}

// Refresh has nothing to do: the password is the credential.
func (*webdav) Refresh(_ context.Context, _ provider.Account, sec provider.Secret) (provider.Secret, error) {
	if sec.Password == "" {
		return sec, fmt.Errorf("no password: %w", provider.ErrSignIn)
	}
	return sec, nil
}

// Logout has nothing to revoke on the server.
func (*webdav) Logout(context.Context, provider.Account, provider.Secret) error { return nil }

// Drives is the folder the address points at.
func (w *webdav) Drives(ctx context.Context, acct provider.Account, sec provider.Secret) ([]provider.Drive, error) {
	q, err := w.quota(ctx, acct, sec)
	if err != nil {
		return nil, err
	}
	name := webaddr.Host(acct.Server)
	if u, err := url.Parse(acct.Server); err == nil {
		if base := path.Base(strings.TrimRight(u.Path, "/")); base != "." && base != "/" {
			name = base + " · " + u.Host
		}
	}
	return []provider.Drive{{ID: "root", Name: name, Kind: provider.KindPersonal, Location: acct.Server, Quota: q}}, nil
}

// Remote is rclone's WebDAV backend with the password.
func (*webdav) Remote(_ provider.Env, acct provider.Account, sec provider.Secret, d provider.Drive) (provider.Remote, error) {
	if sec.Password == "" {
		return provider.Remote{}, fmt.Errorf("no password: %w", provider.ErrSignIn)
	}
	return provider.Remote{
		Backend: "webdav",
		Options: map[string]string{
			"url":    d.Location,
			"vendor": "other",
			"user":   acct.User,
			"pass":   obscure.MustObscure(sec.Password),
		},
	}, nil
}

const quotaBody = `<?xml version="1.0" encoding="utf-8"?>
<d:propfind xmlns:d="DAV:"><d:prop><d:quota-used-bytes/><d:quota-available-bytes/></d:prop></d:propfind>`

type multistatus struct {
	Responses []struct {
		Propstats []struct {
			Prop struct {
				Used      string `xml:"quota-used-bytes"`
				Available string `xml:"quota-available-bytes"`
			} `xml:"prop"`
			Status string `xml:"status"`
		} `xml:"propstat"`
	} `xml:"response"`
}

// quota asks for the folder's RFC 4331 quota, which also checks the login.
// It is nil when the server reports none.
func (w *webdav) quota(ctx context.Context, acct provider.Account, sec provider.Secret) (*provider.Quota, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", strings.TrimRight(acct.Server, "/")+"/", strings.NewReader(quotaBody))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(acct.User, sec.Password)
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("PROPFIND: HTTP %d: %w", resp.StatusCode, provider.ErrSignIn)
	case resp.StatusCode != http.StatusMultiStatus:
		return nil, fmt.Errorf("PROPFIND: HTTP %d", resp.StatusCode)
	}
	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("PROPFIND: %w", err)
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			used, uerr := strconv.ParseInt(strings.TrimSpace(ps.Prop.Used), 10, 64)
			avail, aerr := strconv.ParseInt(strings.TrimSpace(ps.Prop.Available), 10, 64)
			if uerr != nil {
				continue
			}
			q := &provider.Quota{Used: used}
			if aerr == nil && avail >= 0 {
				q.Total = used + avail
			}
			return q, nil
		}
	}
	return nil, nil
}
