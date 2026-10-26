// Package oidcflow implements the OAuth 2.0 authorization code flow with
// PKCE and a loopback redirect (RFC 8252), shared by OIDC-based providers.
package oidcflow

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// DefaultScopes asks for an ID token, profile claims and a refresh token.
var DefaultScopes = []string{oidc.ScopeOpenID, "profile", "email", oidc.ScopeOfflineAccess}

// Client talks to one issuer with one public client ID.
type Client struct {
	Issuer   string
	ClientID string
	Scopes   []string

	provider *oidc.Provider
}

// New discovers the issuer's endpoints.
func New(ctx context.Context, issuer, clientID string, scopes []string) (*Client, error) {
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", issuer, err)
	}
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	return &Client{Issuer: issuer, ClientID: clientID, Scopes: scopes, provider: p}, nil
}

func (c *Client) config(redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    c.ClientID,
		Endpoint:    c.provider.Endpoint(),
		RedirectURL: redirect,
		Scopes:      c.Scopes,
	}
}

// Result is what a successful login yields.
type Result struct {
	Token  *oauth2.Token
	Claims map[string]any // verified ID token claims
}

// Login opens the browser at the authorization endpoint and waits for the
// redirect on a random loopback port.
func (c *Client) Login(ctx context.Context, openBrowser func(string) error) (*Result, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()

	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	cfg := c.config(redirect)
	state := randomString()
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))

	type outcome struct {
		code string
		err  error
	}
	done := make(chan outcome, 1)
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			code, err := callbackCode(r, state)
			if err != nil {
				writePage(w, http.StatusBadRequest, "로그인 실패", err.Error())
			} else {
				writePage(w, http.StatusOK, "로그인 완료", "이 창을 닫아도 됩니다.")
			}
			select {
			case done <- outcome{code, err}:
			default:
			}
		}),
	}
	go srv.Serve(ln)
	defer srv.Close()

	if err := openBrowser(authURL); err != nil {
		return nil, fmt.Errorf("open browser: %w (visit %s manually)", err, authURL)
	}

	var res outcome
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if res.err != nil {
		return nil, res.err
	}

	tok, err := cfg.Exchange(ctx, res.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	claims, err := c.verifyIDToken(ctx, tok)
	if err != nil {
		return nil, err
	}
	return &Result{Token: tok, Claims: claims}, nil
}

// Accepts asks the authorization endpoint, without any user interaction,
// whether it knows this client and allows a loopback redirect. Issuers
// answer an unknown client with an error status or an error redirect
// instead of the usual hop to their sign-in page.
func (c *Client) Accepts(ctx context.Context) (bool, error) {
	const redirect = "http://127.0.0.1:53682/callback"
	authURL := c.config(redirect).AuthCodeURL("probe", oauth2.S256ChallengeOption(oauth2.GenerateVerifier()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return false, err
	}
	hc := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		loc := resp.Header.Get("Location")
		return !(strings.HasPrefix(loc, redirect) && strings.Contains(loc, "error=")), nil
	case resp.StatusCode == http.StatusOK:
		return true, nil // sign-in page rendered directly
	}
	return false, nil
}

// callbackCode validates the redirect and extracts the authorization code.
func callbackCode(r *http.Request, state string) (string, error) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("%s: %s", e, q.Get("error_description"))
	}
	if q.Get("state") != state {
		return "", errors.New("state mismatch")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("missing code")
	}
	return code, nil
}

func (c *Client) verifyIDToken(ctx context.Context, tok *oauth2.Token) (map[string]any, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("no id_token in token response")
	}
	idt, err := c.provider.Verifier(&oidc.Config{ClientID: c.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// Refresh exchanges a refresh token for a new token set.
// Issuers that rotate refresh tokens return a new one, which callers must store.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*oauth2.Token, error) {
	ts := c.config("").TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	tok, err := ts.Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	return tok, nil
}

// Revoke invalidates a token at the issuer's revocation endpoint, if it has one.
func (c *Client) Revoke(ctx context.Context, token, hint string) error {
	var meta struct {
		RevocationEndpoint string `json:"revocation_endpoint"`
	}
	if err := c.provider.Claims(&meta); err != nil || meta.RevocationEndpoint == "" {
		return nil
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}, "client_id": {c.ClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RevocationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("revoke: %s", resp.Status)
	}
	return nil
}

func randomString() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writePage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%[1]s</title>
<body style="font-family:system-ui,sans-serif;display:grid;place-items:center;height:90vh">
<div style="text-align:center"><h2>%[1]s</h2><p>%[2]s</p></div>`, html.EscapeString(title), html.EscapeString(body))
}

// Rejected reports whether the issuer refused a refresh token (expired,
// revoked), as opposed to not being reachable.
func Rejected(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && re.Response != nil && re.Response.StatusCode >= 400 && re.Response.StatusCode < 500
}
