// Package opencloud signs in to OpenCloud (and ownCloud Infinite Scale)
// through its external OIDC issuer and mounts spaces over WebDAV.
package opencloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/auth/oidcflow"
	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/provider/webaddr"
)

// knownClients maps server hosts to a native OIDC client registered for
// this app at their issuer, for issuers that can't use the standard ID.
// Public servers can be added here; others enter their ID under 고급 설정.
var knownClients = map[string]string{}

// standardClientID is the public desktop client OpenCloud's built-in
// identity provider ships with, and the ID its docs tell admins to
// register at external providers.
const standardClientID = "OpenCloudDesktop"

const (
	extraIssuer   = "issuer"
	extraClientID = "client_id"
	optClientID   = "client_id"
)

type openCloud struct {
	httpClient *http.Client
}

func init() {
	provider.Register(&openCloud{httpClient: &http.Client{Timeout: 30 * time.Second}})
}

func (*openCloud) Name() string        { return "opencloud" }
func (*openCloud) Title() string       { return "OpenCloud" }
func (*openCloud) Description() string { return "OpenCloud · ownCloud Infinite Scale 서버" }
func (*openCloud) AddressHint() string { return "예: cloud.example.com 또는 192.168.0.10:9200" }

func (*openCloud) Options() []provider.Option {
	return []provider.Option{{
		Key:      optClientID,
		Label:    "클라이언트 ID",
		Hint:     "비워 두면 자동으로 찾아요",
		Advanced: true,
	}}
}

func (o *openCloud) Login(ctx context.Context, env provider.Env, opt provider.LoginOptions) (provider.Account, provider.Secret, error) {
	server := strings.TrimRight(opt.Server.URL, "/")
	issuer, err := o.discoverIssuer(ctx, server)
	if err != nil {
		return provider.Account{}, provider.Secret{}, provider.Explain("서버에서 로그인 정보를 받지 못했어요.", "", err)
	}
	oc, err := o.pickClient(ctx, issuer, server, strings.TrimSpace(opt.Values[optClientID]))
	if err != nil {
		return provider.Account{}, provider.Secret{}, err
	}
	res, err := oc.Login(ctx, env.OpenBrowser)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return provider.Account{}, provider.Secret{}, err
		}
		return provider.Account{}, provider.Secret{}, provider.Explain("로그인을 마치지 못했어요.", "다시 시도해 주세요.", err)
	}
	sec := secretFrom(res.Token.AccessToken, res.Token.RefreshToken, res.Token.Expiry)

	// Asking the server who we are proves it accepts this client's tokens.
	me, err := o.me(ctx, server, sec.AccessToken)
	if err != nil {
		return provider.Account{}, provider.Secret{}, provider.Explain("로그인은 됐지만 서버가 이 로그인을 받아 주지 않아요.",
			"이 서버에 계정이 있는지, 관리자가 앱 접근을 허용했는지 확인해 주세요.", err)
	}
	acct := provider.Account{
		Provider: o.Name(),
		Server:   server,
		User:     me.Username,
		Display:  me.DisplayName,
		Extra:    map[string]string{extraIssuer: issuer, extraClientID: oc.ClientID},
	}
	return acct, sec, nil
}

// pickClient finds a client ID the issuer accepts: the user's choice if
// given, otherwise a known one for this server, then the standard ID.
func (o *openCloud) pickClient(ctx context.Context, issuer, server, explicit string) (*oidcflow.Client, error) {
	var ids []string
	if explicit != "" {
		ids = []string{explicit}
	} else {
		if id := knownClients[hostOf(server)]; id != "" {
			ids = append(ids, id)
		}
		ids = append(ids, standardClientID)
	}
	var probeErr error
	for _, id := range ids {
		oc, err := oidcflow.New(ctx, issuer, id, nil)
		if err != nil {
			return nil, provider.Explain(fmt.Sprintf("로그인 서버(%s)에 연결할 수 없어요.", hostOf(issuer)), "", err)
		}
		ok, err := oc.Accepts(ctx)
		if err != nil {
			probeErr = err
			continue
		}
		if ok {
			return oc, nil
		}
	}
	if explicit != "" {
		return nil, provider.Explain(fmt.Sprintf("로그인 서버가 클라이언트 ID '%s'를 받아 주지 않아요.", explicit),
			"고급 설정의 클라이언트 ID를 확인해 주세요.", probeErr)
	}
	return nil, provider.Explain("로그인 서버가 이 앱을 아직 몰라요.",
		fmt.Sprintf("관리자에게 '%s' 클라이언트 등록을 요청하거나, 고급 설정에 받은 클라이언트 ID를 넣어 주세요.", standardClientID), probeErr)
}

type statusInfo struct {
	ProductName    string `json:"productname"`
	ProductVersion string `json:"productversion"`
	VersionString  string `json:"versionstring"`
}

func (s statusInfo) product() string {
	v := s.ProductVersion
	if v == "" {
		v = s.VersionString
	}
	return strings.TrimSpace(s.ProductName + " " + v)
}

func isOpenCloud(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "opencloud") || strings.Contains(n, "infinite scale")
}

func (o *openCloud) Resolve(ctx context.Context, input string) (provider.Server, error) {
	bases, err := webaddr.Candidates(input)
	if err != nil {
		return provider.Server{}, err
	}
	var lastErr error
	for _, base := range bases {
		st, err := o.status(ctx, base)
		if err != nil {
			lastErr = err
			continue
		}
		if !isOpenCloud(st.ProductName) {
			return provider.Server{}, provider.Explain(fmt.Sprintf("이 주소는 OpenCloud가 아니라 %s 서버예요.", st.ProductName),
				"OpenCloud 서버 주소를 넣거나, 이전 단계에서 다른 서비스를 골라 주세요.", nil)
		}
		// The server knows its public address; use that so accounts and
		// mount URLs agree however the user typed it.
		canon := base
		if pub, err := o.publicURL(ctx, base); err == nil && pub != "" {
			canon = pub
		}
		if canon != base {
			if _, err := o.status(ctx, canon); err != nil {
				return provider.Server{}, provider.Explain(fmt.Sprintf("서버가 알려 준 주소 %s 에 연결할 수 없어요.", hostOf(canon)),
					"이 컴퓨터에서 그 주소로 접속되는지 확인해 주세요.", err)
			}
		}
		return provider.Server{URL: canon, Product: st.product()}, nil
	}
	return provider.Server{}, provider.Explain("이 주소에서 OpenCloud 서버를 찾지 못했어요.", "주소를 다시 확인해 주세요.", lastErr)
}

func (o *openCloud) status(ctx context.Context, base string) (statusInfo, error) {
	var st statusInfo
	if err := o.getJSON(ctx, base+"/status.php", &st); err != nil {
		return st, err
	}
	if st.ProductName == "" {
		return st, errors.New("status.php has no product name")
	}
	return st, nil
}

// publicURL reads the address the server is configured with (OC_URL),
// which its web client config exposes.
func (o *openCloud) publicURL(ctx context.Context, base string) (string, error) {
	var cfg struct {
		Server string `json:"server"`
	}
	if err := o.getJSON(ctx, base+"/config.json", &cfg); err != nil {
		return "", err
	}
	if cfg.Server == "" {
		return "", nil
	}
	u, err := url.Parse(cfg.Server)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("config.json: bad server %q", cfg.Server)
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

func (o *openCloud) getJSON(ctx context.Context, rawURL string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (o *openCloud) client(ctx context.Context, acct provider.Account) (*oidcflow.Client, error) {
	issuer, clientID := acct.Extra[extraIssuer], acct.Extra[extraClientID]
	if issuer == "" || clientID == "" {
		return nil, fmt.Errorf("account is missing issuer or client id: %w", provider.ErrSignIn)
	}
	return oidcflow.New(ctx, issuer, clientID, nil)
}

func (o *openCloud) Refresh(ctx context.Context, acct provider.Account, sec provider.Secret) (provider.Secret, error) {
	if sec.RefreshToken == "" {
		return provider.Secret{}, fmt.Errorf("no refresh token: %w", provider.ErrSignIn)
	}
	oc, err := o.client(ctx, acct)
	if err != nil {
		return provider.Secret{}, err
	}
	tok, err := oc.Refresh(ctx, sec.RefreshToken)
	if err != nil {
		if oidcflow.Rejected(err) {
			err = fmt.Errorf("%w: %w", provider.ErrSignIn, err)
		}
		return provider.Secret{}, err
	}
	return secretFrom(tok.AccessToken, tok.RefreshToken, tok.Expiry), nil
}

func (o *openCloud) Logout(ctx context.Context, acct provider.Account, sec provider.Secret) error {
	if sec.RefreshToken == "" {
		return nil
	}
	oc, err := o.client(ctx, acct)
	if err != nil {
		return err
	}
	return oc.Revoke(ctx, sec.RefreshToken, "refresh_token")
}

func (o *openCloud) Drives(ctx context.Context, acct provider.Account, sec provider.Secret) ([]provider.Drive, error) {
	var body struct {
		Value []graphDrive `json:"value"`
	}
	if err := o.graph(ctx, acct.Server, sec.AccessToken, "/graph/v1.0/me/drives", &body); err != nil {
		return nil, err
	}
	return drivesFrom(body.Value), nil
}

func (o *openCloud) Remote(env provider.Env, acct provider.Account, _ provider.Secret, d provider.Drive) (provider.Remote, error) {
	if d.Location == "" {
		return provider.Remote{}, fmt.Errorf("drive %q has no WebDAV URL", d.Name)
	}
	if env.TokenCommand == nil {
		return provider.Remote{}, errors.New("token command is required")
	}
	return provider.Remote{
		Backend: "webdav",
		Options: map[string]string{
			"url":                  d.Location,
			"vendor":               "infinitescale",
			"bearer_token_command": joinCommand(env.TokenCommand(acct)),
		},
	}, nil
}

type graphDrive struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	DriveType string `json:"driveType"`
	Root      struct {
		WebDavURL string `json:"webDavUrl"`
		Deleted   *struct {
			State string `json:"state"`
		} `json:"deleted"`
	} `json:"root"`
	Quota *struct {
		Total int64 `json:"total"` // 0 means no limit
		Used  int64 `json:"used"`
	} `json:"quota"`
}

// drivesFrom keeps the drives worth mounting, personal space first.
func drivesFrom(in []graphDrive) []provider.Drive {
	var personal, rest []provider.Drive
	for _, g := range in {
		if g.Root.WebDavURL == "" || g.Root.Deleted != nil {
			continue
		}
		d := provider.Drive{ID: g.ID, Name: g.Name, Location: g.Root.WebDavURL}
		if q := g.Quota; q != nil {
			d.Quota = &provider.Quota{Used: max(q.Used, 0), Total: max(q.Total, 0)}
		}
		switch g.DriveType {
		case "personal":
			d.Kind = provider.KindPersonal
			personal = append(personal, d)
			continue
		case "project":
			d.Kind = provider.KindProject
		case "virtual":
			d.Kind = provider.KindShares
		default:
			// mountpoints are individual shares already reachable via the shares jail
			continue
		}
		rest = append(rest, d)
	}
	return append(personal, rest...)
}

type meResponse struct {
	Username    string
	DisplayName string
}

func (o *openCloud) me(ctx context.Context, server, token string) (meResponse, error) {
	var body struct {
		DisplayName              string `json:"displayName"`
		OnPremisesSamAccountName string `json:"onPremisesSamAccountName"`
		Mail                     string `json:"mail"`
	}
	if err := o.graph(ctx, server, token, "/graph/v1.0/me", &body); err != nil {
		return meResponse{}, err
	}
	user := body.OnPremisesSamAccountName
	if user == "" {
		user = body.Mail
	}
	if user == "" {
		return meResponse{}, errors.New("server returned no user name")
	}
	return meResponse{Username: user, DisplayName: body.DisplayName}, nil
}

func (o *openCloud) graph(ctx context.Context, server, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// discoverIssuer asks the server's webfinger endpoint which OIDC issuer it trusts.
func (o *openCloud) discoverIssuer(ctx context.Context, server string) (string, error) {
	const rel = "http://openid.net/specs/connect/1.0/issuer"
	q := url.Values{"resource": {server}, "rel": {rel}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/.well-known/webfinger?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webfinger: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("webfinger: %s (is this an OpenCloud server?)", resp.Status)
	}
	var wf struct {
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wf); err != nil {
		return "", fmt.Errorf("webfinger: %w", err)
	}
	for _, l := range wf.Links {
		if l.Rel == rel && l.Href != "" {
			return l.Href, nil
		}
	}
	return "", errors.New("webfinger: no OIDC issuer advertised")
}

func secretFrom(access, refresh string, expiry time.Time) provider.Secret {
	return provider.Secret{AccessToken: access, RefreshToken: refresh, Expiry: expiry}
}

func hostOf(server string) string {
	u, err := url.Parse(server)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// joinCommand renders argv in rclone's space separated list syntax,
// quoting elements that contain spaces (e.g. "C:\Program Files\...").
func joinCommand(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \"") {
			a = `"` + strings.ReplaceAll(a, `"`, `""`) + `"`
		}
		parts[i] = a
	}
	return strings.Join(parts, " ")
}

// InstantSupported checks the server's capabilities for tus uploads.
func (o *openCloud) InstantSupported(ctx context.Context, acct provider.Account, sec provider.Secret) bool {
	var caps struct {
		OCS struct {
			Data struct {
				Capabilities struct {
					Files struct {
						TusSupport *struct {
							Version string `json:"version"`
						} `json:"tus_support"`
					} `json:"files"`
				} `json:"capabilities"`
			} `json:"data"`
		} `json:"ocs"`
	}
	if err := o.graph(ctx, acct.Server, sec.AccessToken, "/ocs/v1.php/cloud/capabilities?format=json", &caps); err != nil {
		return false
	}
	t := caps.OCS.Data.Capabilities.Files.TusSupport
	return t != nil && t.Version != ""
}

// InstantFolder allows instant uploads into personal and project spaces;
// the shares view is a virtual tree that uploads can't start from.
func (o *openCloud) InstantFolder(d provider.Drive) string {
	if d.Kind == provider.KindPersonal || d.Kind == provider.KindProject {
		return d.Location
	}
	return ""
}

var _ provider.Instant = (*openCloud)(nil)
