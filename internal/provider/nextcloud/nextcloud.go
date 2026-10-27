// Package nextcloud connects Nextcloud servers.
//
// Signing in uses Nextcloud's Login Flow v2: the browser shows Nextcloud's
// own login and grant page, and the app receives an app password, which
// never passes through this app's window. Drives are mounted with rclone's
// WebDAV backend in its Nextcloud mode (chunked uploads).
package nextcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/provider/webaddr"
)

// extraUserID is the account's user ID, which names its WebDAV folder; it
// can differ from the login name (an email address, say).
const extraUserID = "user_id"

// userAgent names the app password in Nextcloud's list of devices.
const userAgent = "Proto Easy Mount"

type nextcloud struct {
	http *http.Client
	poll time.Duration // how often the login flow is polled
}

func init() {
	provider.Register(&nextcloud{http: &http.Client{Timeout: 30 * time.Second}, poll: 2 * time.Second})
}

func (*nextcloud) Name() string               { return "nextcloud" }
func (*nextcloud) Title() string              { return "Nextcloud" }
func (*nextcloud) Description() string        { return "Nextcloud 서버 · 브라우저에서 로그인" }
func (*nextcloud) AddressHint() string        { return "예: cloud.example.com 또는 192.168.0.10" }
func (*nextcloud) Options() []provider.Option { return nil }

type statusInfo struct {
	Installed     bool   `json:"installed"`
	Maintenance   bool   `json:"maintenance"`
	VersionString string `json:"versionstring"`
	ProductName   string `json:"productname"`
}

// Resolve finds the Nextcloud server behind what the user typed.
func (n *nextcloud) Resolve(ctx context.Context, input string) (provider.Server, error) {
	bases, err := webaddr.Candidates(input)
	if err != nil {
		return provider.Server{}, err
	}
	var lastErr error
	for _, base := range bases {
		var st statusInfo
		if err := n.getJSON(ctx, base+"/status.php", nil, &st); err != nil {
			lastErr = err
			continue
		}
		if !strings.Contains(strings.ToLower(st.ProductName), "nextcloud") {
			name := st.ProductName
			if name == "" {
				name = "다른 종류의"
			}
			return provider.Server{}, provider.Explain(fmt.Sprintf("이 주소는 Nextcloud가 아니라 %s 서버예요.", name),
				"Nextcloud 서버 주소를 넣거나, 이전 단계에서 다른 서비스를 골라 주세요.", nil)
		}
		if st.Maintenance {
			return provider.Server{}, provider.Explain("Nextcloud 서버가 점검 중이에요.", "점검이 끝난 뒤 다시 시도해 주세요.", nil)
		}
		return provider.Server{URL: base, Product: "Nextcloud " + st.VersionString}, nil
	}
	return provider.Server{}, provider.Explain("이 주소에서 Nextcloud 서버를 찾지 못했어요.", "주소를 다시 확인해 주세요.", lastErr)
}

type flowStart struct {
	Poll struct {
		Token    string `json:"token"`
		Endpoint string `json:"endpoint"`
	} `json:"poll"`
	Login string `json:"login"`
}

type flowResult struct {
	Server      string `json:"server"`
	LoginName   string `json:"loginName"`
	AppPassword string `json:"appPassword"`
}

// Login runs Login Flow v2: open Nextcloud's grant page in the browser and
// poll until the user has allowed access (or ctx ends).
func (n *nextcloud) Login(ctx context.Context, env provider.Env, opt provider.LoginOptions) (provider.Account, provider.Secret, error) {
	base := opt.Server.URL
	var start flowStart
	if err := n.do(ctx, http.MethodPost, base+"/index.php/login/v2", nil, nil, &start); err != nil {
		return provider.Account{}, provider.Secret{}, provider.Explain("Nextcloud 로그인을 시작하지 못했어요.",
			"서버가 Login Flow v2를 지원하는지(Nextcloud 16 이상) 확인해 주세요.", err)
	}
	if start.Login == "" || start.Poll.Token == "" || start.Poll.Endpoint == "" {
		return provider.Account{}, provider.Secret{}, errors.New("nextcloud: incomplete login flow answer")
	}
	if env.OpenBrowser == nil {
		return provider.Account{}, provider.Secret{}, errors.New("nextcloud: no browser to sign in with")
	}
	if err := env.OpenBrowser(start.Login); err != nil {
		return provider.Account{}, provider.Secret{}, fmt.Errorf("open browser: %w", err)
	}

	res, err := n.pollFlow(ctx, start.Poll.Endpoint, start.Poll.Token)
	if err != nil {
		return provider.Account{}, provider.Secret{}, err
	}
	// Keep the address the user reached the server by: the flow's "server"
	// is its configured public URL, which may not resolve from here.
	sec := provider.Secret{Password: res.AppPassword}
	acct := provider.Account{Provider: n.Name(), Server: base, User: res.LoginName}
	u, err := n.user(ctx, acct, sec)
	if err != nil {
		return provider.Account{}, provider.Secret{}, err
	}
	acct.Display = u.DisplayName
	acct.Extra = map[string]string{extraUserID: u.ID}
	return acct, sec, nil
}

// pollFlow waits for the user to grant access; Nextcloud answers 404 until then.
func (n *nextcloud) pollFlow(ctx context.Context, endpoint, token string) (flowResult, error) {
	form := url.Values{"token": {token}}.Encode()
	t := time.NewTicker(n.poll)
	defer t.Stop()
	for {
		var res flowResult
		err := n.do(ctx, http.MethodPost, endpoint, strings.NewReader(form),
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, &res)
		var se *statusError
		switch {
		case err == nil && res.AppPassword != "":
			return res, nil
		case err == nil, errors.As(err, &se) && se.code == http.StatusNotFound:
			// not granted yet
		default:
			return flowResult{}, err
		}
		select {
		case <-ctx.Done():
			return flowResult{}, ctx.Err()
		case <-t.C:
		}
	}
}

// Refresh has nothing to do: an app password stays valid until revoked.
func (*nextcloud) Refresh(_ context.Context, _ provider.Account, sec provider.Secret) (provider.Secret, error) {
	if sec.Password == "" {
		return sec, fmt.Errorf("no app password: %w", provider.ErrSignIn)
	}
	return sec, nil
}

// Logout revokes the app password on the server.
func (n *nextcloud) Logout(ctx context.Context, acct provider.Account, sec provider.Secret) error {
	if sec.Password == "" {
		return nil
	}
	return n.ocs(ctx, http.MethodDelete, acct, sec, "/ocs/v2.php/core/apppassword", nil)
}

type userInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"display-name"`
	Quota       struct {
		Used  int64 `json:"used"`
		Quota int64 `json:"quota"` // negative: no limit
	} `json:"quota"`
}

func (n *nextcloud) user(ctx context.Context, acct provider.Account, sec provider.Secret) (userInfo, error) {
	var out struct {
		OCS struct {
			Data userInfo `json:"data"`
		} `json:"ocs"`
	}
	if err := n.ocs(ctx, http.MethodGet, acct, sec, "/ocs/v2.php/cloud/user?format=json", &out); err != nil {
		return userInfo{}, err
	}
	if out.OCS.Data.ID == "" {
		return userInfo{}, errors.New("nextcloud: user info has no id")
	}
	return out.OCS.Data, nil
}

// Drives is the user's files: Nextcloud has one tree per user, group
// folders and shares included.
func (n *nextcloud) Drives(ctx context.Context, acct provider.Account, sec provider.Secret) ([]provider.Drive, error) {
	u, err := n.user(ctx, acct, sec)
	if err != nil {
		return nil, err
	}
	name := u.DisplayName
	if name == "" {
		name = acct.User
	}
	d := provider.Drive{
		ID:       "files",
		Name:     name,
		Kind:     provider.KindPersonal,
		Location: acct.Server + "/remote.php/dav/files/" + url.PathEscape(u.ID),
		Quota:    &provider.Quota{Used: u.Quota.Used},
	}
	if u.Quota.Quota > 0 {
		d.Quota.Total = u.Quota.Quota
	}
	return []provider.Drive{d}, nil
}

// Remote is rclone's WebDAV backend in Nextcloud mode with the app password.
func (*nextcloud) Remote(_ provider.Env, acct provider.Account, sec provider.Secret, d provider.Drive) (provider.Remote, error) {
	if sec.Password == "" {
		return provider.Remote{}, fmt.Errorf("no app password: %w", provider.ErrSignIn)
	}
	return provider.Remote{
		Backend: "webdav",
		Options: map[string]string{
			"url":    d.Location,
			"vendor": "nextcloud",
			"user":   acct.User,
			"pass":   obscure.MustObscure(sec.Password),
		},
	}, nil
}

// ocs calls the OCS API with the app password.
func (n *nextcloud) ocs(ctx context.Context, method string, acct provider.Account, sec provider.Secret, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, acct.Server+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(acct.User, sec.Password)
	req.Header.Set("OCS-APIRequest", "true")
	req.Header.Set("Accept", "application/json")
	err = n.send(req, out)
	var se *statusError
	if errors.As(err, &se) && se.code == http.StatusUnauthorized {
		return fmt.Errorf("%w: %v", provider.ErrSignIn, err) // the app password was revoked
	}
	return err
}

func (n *nextcloud) getJSON(ctx context.Context, rawURL string, hdr map[string]string, out any) error {
	return n.do(ctx, http.MethodGet, rawURL, nil, hdr, out)
}

func (n *nextcloud) do(ctx context.Context, method, rawURL string, body io.Reader, hdr map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return n.send(req, out)
}

type statusError struct {
	url  string
	code int
}

func (e *statusError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.url, e.code) }

func (n *nextcloud) send(req *http.Request, out any) error {
	req.Header.Set("User-Agent", userAgent)
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{url: req.URL.Redacted(), code: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
