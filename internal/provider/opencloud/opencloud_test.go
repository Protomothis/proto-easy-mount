package opencloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

func TestDrivesFrom(t *testing.T) {
	var in []graphDrive
	raw := `[
	 {"id":"p1","name":"Alpha","driveType":"project","root":{"webDavUrl":"https://s/dav/spaces/p1"},"quota":{"total":1000,"used":250,"remaining":750}},
	 {"id":"me","name":"Alice","driveType":"personal","root":{"webDavUrl":"https://s/dav/spaces/me"},"quota":{"remaining":9223372036854775807,"state":"normal","total":0,"used":42}},
	 {"id":"gone","name":"Old","driveType":"project","root":{"webDavUrl":"https://s/dav/spaces/gone","deleted":{"state":"trashed"}}},
	 {"id":"sh","name":"Shares","driveType":"virtual","root":{"webDavUrl":"https://s/dav/spaces/sh"}},
	 {"id":"mp","name":"one share","driveType":"mountpoint","root":{"webDavUrl":"https://s/dav/spaces/mp"}}
	]`
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		t.Fatal(err)
	}
	got := drivesFrom(in)
	want := []struct {
		id   string
		kind provider.DriveKind
	}{{"me", provider.KindPersonal}, {"p1", provider.KindProject}, {"sh", provider.KindShares}}
	if len(got) != len(want) {
		t.Fatalf("got %d drives: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Kind != w.kind {
			t.Errorf("drive %d = %s/%s, want %s/%s", i, got[i].ID, got[i].Kind, w.id, w.kind)
		}
	}
	if q := got[0].Quota; q == nil || !q.Unlimited() || q.Used != 42 {
		t.Errorf("personal quota = %+v, want unlimited with 42 used", q)
	}
	if q := got[1].Quota; q == nil || q.Unlimited() || q.Free() != 750 {
		t.Errorf("project quota = %+v, want 750 free of 1000", q)
	}
	if got[2].Quota != nil {
		t.Errorf("shares should have no quota: %+v", got[2].Quota)
	}
}

func TestCandidates(t *testing.T) {
	for in, want := range map[string][]string{
		"cloud.example.com":           {"https://cloud.example.com"},
		" https://drive.example.com/": {"https://drive.example.com"},
		"http://localhost:9200":       {"http://localhost:9200"},
		"192.168.0.10:9200":           {"https://192.168.0.10:9200", "http://192.168.0.10:9200"},
		"nas.local/cloud/":            {"https://nas.local/cloud", "http://nas.local/cloud"},
		"nas":                         {"https://nas", "http://nas"},
	} {
		got, err := candidates(in)
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("candidates(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "  ", "ftp://x.example.com"} {
		if _, err := candidates(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeServer serves status.php and config.json like OpenCloud does.
func fakeServer(t *testing.T, product, public string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status.php":
			fmt.Fprintf(w, `{"productname":%q,"productversion":"7.2.4"}`, product)
		case "/config.json":
			fmt.Fprintf(w, `{"server":%q}`, public)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveFollowsPublicAddress(t *testing.T) {
	public := fakeServer(t, "OpenCloud", "")
	// The user typed a different address for the same server.
	local := fakeServer(t, "OpenCloud", public.URL+"/")

	o := &openCloud{httpClient: http.DefaultClient}
	got, err := o.Resolve(context.Background(), local.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != public.URL || got.Product != "OpenCloud 7.2.4" {
		t.Fatalf("resolved %+v, want %s", got, public.URL)
	}
}

func TestResolveExplainsProblems(t *testing.T) {
	o := &openCloud{httpClient: http.DefaultClient}
	ctx := context.Background()

	nc := fakeServer(t, "Nextcloud", "")
	_, err := o.Resolve(ctx, nc.URL)
	if ue := provider.AsUserError(err); !strings.Contains(ue.Msg, "Nextcloud") {
		t.Errorf("wrong product: %v", err)
	}

	unreachable := fakeServer(t, "OpenCloud", "http://127.0.0.1:1/")
	_, err = o.Resolve(ctx, unreachable.URL)
	if ue := provider.AsUserError(err); !strings.Contains(ue.Msg, "127.0.0.1") {
		t.Errorf("unreachable public address: %v", err)
	}

	_, err = o.Resolve(ctx, "http://127.0.0.1:1")
	if ue := provider.AsUserError(err); !strings.Contains(ue.Msg, "찾지 못했어요") {
		t.Errorf("no server: %v", err)
	}
}

// fakeIssuer is an OIDC issuer that knows only the given client IDs.
func fakeIssuer(t *testing.T, known ...string) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
				srv.URL, srv.URL+"/authorize", srv.URL+"/token", srv.URL+"/keys")
		case "/authorize":
			for _, k := range known {
				if r.URL.Query().Get("client_id") == k {
					http.Redirect(w, r, "/login", http.StatusFound)
					return
				}
			}
			http.Error(w, "invalid_client", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPickClient(t *testing.T) {
	o := &openCloud{httpClient: http.DefaultClient}
	ctx := context.Background()

	std := fakeIssuer(t, standardClientID)
	oc, err := o.pickClient(ctx, std.URL, "https://cloud.example.com", "")
	if err != nil || oc.ClientID != standardClientID {
		t.Fatalf("standard client: %v, %v", oc, err)
	}

	custom := fakeIssuer(t, "abc123")
	if oc, err := o.pickClient(ctx, custom.URL, "https://cloud.example.com", "abc123"); err != nil || oc.ClientID != "abc123" {
		t.Fatalf("explicit client: %v, %v", oc, err)
	}
	_, err = o.pickClient(ctx, custom.URL, "https://cloud.example.com", "")
	if ue := provider.AsUserError(err); !strings.Contains(ue.Hint, standardClientID) {
		t.Fatalf("unknown app not explained: %v", err)
	}
}

// joinCommand must survive rclone's own parser, including Windows paths with spaces.
func TestJoinCommandRoundTrip(t *testing.T) {
	argv := []string{`C:\Program Files\Proto Easy Mount\proto-easy-mount.exe`, "token", "--account", "opencloud-drive.example.com-alice"}
	var l fs.SpaceSepList
	if err := l.Set(joinCommand(argv)); err != nil {
		t.Fatal(err)
	}
	if len(l) != len(argv) {
		t.Fatalf("parsed %q", []string(l))
	}
	for i := range argv {
		if l[i] != argv[i] {
			t.Errorf("arg %d = %q, want %q", i, l[i], argv[i])
		}
	}
}

func TestDiscoverIssuerAndMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/webfinger":
			w.Write([]byte(`{"links":[{"rel":"http://openid.net/specs/connect/1.0/issuer","href":"https://idp.example"}]}`))
		case "/graph/v1.0/me":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"displayName":"Alice A","onPremisesSamAccountName":"alice"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	o := &openCloud{httpClient: srv.Client()}
	ctx := context.Background()
	iss, err := o.discoverIssuer(ctx, srv.URL)
	if err != nil || iss != "https://idp.example" {
		t.Fatalf("issuer = %q, %v", iss, err)
	}
	me, err := o.me(ctx, srv.URL, "good")
	if err != nil || me.Username != "alice" || me.DisplayName != "Alice A" {
		t.Fatalf("me = %+v, %v", me, err)
	}
	if _, err := o.me(ctx, srv.URL, "bad"); err == nil {
		t.Fatal("rejected token accepted")
	}
}

func TestRemote(t *testing.T) {
	o := &openCloud{}
	env := provider.Env{TokenCommand: func(a provider.Account) []string { return []string{"pem", "token", "--account", a.ID} }}
	r, err := o.Remote(env, provider.Account{ID: "x"}, provider.Drive{Name: "Alice", Location: "https://s/dav/spaces/me"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Backend != "webdav" || r.Options["vendor"] != "infinitescale" || r.Options["url"] != "https://s/dav/spaces/me" ||
		r.Options["bearer_token_command"] != "pem token --account x" {
		t.Fatalf("remote = %+v", r)
	}
}

func TestInstantSupported(t *testing.T) {
	for _, c := range []struct {
		body string
		want bool
	}{
		{`{"ocs":{"data":{"capabilities":{"files":{"tus_support":{"version":"1.0.0"}}}}}}`, true},
		{`{"ocs":{"data":{"capabilities":{"files":{}}}}}`, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/ocs/v1.php/cloud/capabilities") || r.Header.Get("Authorization") != "Bearer tok" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(c.body))
		}))
		o := &openCloud{httpClient: srv.Client()}
		got := o.InstantSupported(context.Background(), provider.Account{Server: srv.URL}, provider.Secret{AccessToken: "tok"})
		srv.Close()
		if got != c.want {
			t.Errorf("%s: got %v", c.body, got)
		}
	}
	o := &openCloud{}
	if o.InstantFolder(provider.Drive{Kind: provider.KindShares, Location: "x"}) != "" ||
		o.InstantFolder(provider.Drive{Kind: provider.KindProject, Location: "x"}) != "x" {
		t.Error("InstantFolder kinds")
	}
}
