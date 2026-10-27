package nextcloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

// fakeNextcloud speaks enough of Nextcloud: status.php, Login Flow v2
// (granted after a couple of polls), the OCS user and app password revoke.
type fakeNextcloud struct {
	product string
	polls   atomic.Int32
	revoked atomic.Bool
	srv     *httptest.Server
}

func newFake(t *testing.T, product string) *fakeNextcloud {
	f := &fakeNextcloud{product: product}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNextcloud) serve(w http.ResponseWriter, r *http.Request) {
	authed := func() bool {
		u, p, ok := r.BasicAuth()
		return ok && u == "alice@example.com" && p == "app-pass" && r.Header.Get("OCS-APIRequest") == "true"
	}
	switch {
	case r.URL.Path == "/status.php":
		json.NewEncoder(w).Encode(map[string]any{"installed": true, "versionstring": "30.0.1", "productname": f.product})
	case r.URL.Path == "/index.php/login/v2" && r.Method == http.MethodPost:
		if r.Header.Get("User-Agent") != userAgent {
			http.Error(w, "no agent", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"poll":  map[string]string{"token": "tok", "endpoint": f.srv.URL + "/login/v2/poll"},
			"login": f.srv.URL + "/login/v2/flow/abc",
		})
	case r.URL.Path == "/login/v2/poll":
		r.ParseForm()
		if r.Form.Get("token") != "tok" || f.polls.Add(1) < 3 {
			http.NotFound(w, r) // not granted yet
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"server": "https://public.example.com", "loginName": "alice@example.com", "appPassword": "app-pass"})
	case r.URL.Path == "/ocs/v2.php/cloud/user" && authed():
		json.NewEncoder(w).Encode(map[string]any{"ocs": map[string]any{"data": map[string]any{
			"id": "alice", "display-name": "Alice", "quota": map[string]any{"used": 1500, "quota": 10000},
		}}})
	case r.URL.Path == "/ocs/v2.php/core/apppassword" && r.Method == http.MethodDelete && authed():
		f.revoked.Store(true)
	default:
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func testProvider() *nextcloud {
	return &nextcloud{http: &http.Client{Timeout: 5 * time.Second}, poll: 10 * time.Millisecond}
}

func TestLoginFlowAndDrives(t *testing.T) {
	f := newFake(t, "Nextcloud")
	n := testProvider()
	ctx := context.Background()

	srv, err := n.Resolve(ctx, f.srv.URL)
	if err != nil || srv.URL != f.srv.URL || srv.Product != "Nextcloud 30.0.1" {
		t.Fatalf("resolve = %+v, %v", srv, err)
	}
	var opened string
	env := provider.Env{OpenBrowser: func(u string) error { opened = u; return nil }}
	acct, sec, err := n.Login(ctx, env, provider.LoginOptions{Server: srv})
	if err != nil {
		t.Fatal(err)
	}
	if opened != f.srv.URL+"/login/v2/flow/abc" {
		t.Fatalf("browser opened %q", opened)
	}
	// the address the user reached it by, not the server's public URL
	if acct.Server != f.srv.URL || acct.User != "alice@example.com" || acct.Display != "Alice" || acct.Extra[extraUserID] != "alice" {
		t.Fatalf("account = %+v", acct)
	}
	if sec.Password != "app-pass" || sec.RefreshToken != "" {
		t.Fatalf("secret = %+v", sec)
	}

	drives, err := n.Drives(ctx, acct, sec)
	if err != nil || len(drives) != 1 {
		t.Fatalf("drives = %v, %v", drives, err)
	}
	d := drives[0]
	if d.Location != f.srv.URL+"/remote.php/dav/files/alice" || d.Quota == nil || d.Quota.Total != 10000 || d.Quota.Used != 1500 {
		t.Fatalf("drive = %+v (quota %+v)", d, d.Quota)
	}
	r, err := n.Remote(provider.Env{}, acct, sec, d)
	if err != nil || r.Backend != "webdav" || r.Options["vendor"] != "nextcloud" || r.Options["user"] != "alice@example.com" {
		t.Fatalf("remote = %+v, %v", r, err)
	}
	if pass, err := obscure.Reveal(r.Options["pass"]); err != nil || pass != "app-pass" {
		t.Fatalf("rclone pass not the obscured app password: %v", err)
	}

	if err := n.Logout(ctx, acct, sec); err != nil || !f.revoked.Load() {
		t.Fatalf("logout: %v, revoked %v", err, f.revoked.Load())
	}
}

// A revoked app password means signing in again, not a network problem.
func TestRevokedAppPasswordNeedsSignIn(t *testing.T) {
	f := newFake(t, "Nextcloud")
	n := testProvider()
	acct := provider.Account{Server: f.srv.URL, User: "alice@example.com"}
	_, err := n.Drives(context.Background(), acct, provider.Secret{Password: "revoked"})
	if !provider.NeedsSignIn(err) {
		t.Fatalf("err = %v, want a sign-in error", err)
	}
}

func TestResolveRejectsOtherServers(t *testing.T) {
	f := newFake(t, "OpenCloud")
	_, err := testProvider().Resolve(context.Background(), f.srv.URL)
	var ue *provider.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "OpenCloud") {
		t.Fatalf("err = %v", err)
	}
}

// Cancelling while the browser is open ends the wait.
func TestLoginCancelled(t *testing.T) {
	f := newFake(t, "Nextcloud")
	n := testProvider()
	f.polls.Store(-1 << 20) // never granted
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	env := provider.Env{OpenBrowser: func(string) error { return nil }}
	_, _, err := n.Login(ctx, env, provider.LoginOptions{Server: provider.Server{URL: f.srv.URL}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}
