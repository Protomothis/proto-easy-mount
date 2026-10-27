package webdav

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/object"
	xwebdav "golang.org/x/net/webdav"

	_ "github.com/rclone/rclone/backend/webdav"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

// davServer is a real WebDAV server (golang.org/x/net/webdav, in memory)
// under /dav behind basic auth.
func davServer(t *testing.T) *httptest.Server {
	h := &xwebdav.Handler{Prefix: "/dav", FileSystem: xwebdav.NewMemFS(), LockSystem: xwebdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "alice" || p != "s3cret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testProvider() *webdav { return &webdav{http: &http.Client{Timeout: 5 * time.Second}} }

func TestLoginDrivesAndRcloneRemote(t *testing.T) {
	srv := davServer(t)
	w := testProvider()
	ctx := context.Background()

	s, err := w.Resolve(ctx, srv.URL+"/dav/")
	if err != nil || s.URL != srv.URL+"/dav" {
		t.Fatalf("resolve = %+v, %v", s, err)
	}
	acct, sec, err := w.Login(ctx, provider.Env{}, provider.LoginOptions{Server: s,
		Values: map[string]string{optUser: "alice", optPassword: "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	drives, err := w.Drives(ctx, acct, sec)
	if err != nil || len(drives) != 1 || !strings.HasPrefix(drives[0].Name, "dav · ") {
		t.Fatalf("drives = %+v, %v", drives, err)
	}

	// The remote rclone gets really reaches the server with the password.
	r, err := w.Remote(provider.Env{}, acct, sec, drives[0])
	if err != nil {
		t.Fatal(err)
	}
	ri, err := fs.Find(r.Backend)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ri.NewFs(ctx, "test", r.Root, configmap.Simple(r.Options))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("hello webdav")
	if _, err := f.Put(ctx, bytes.NewReader(data), object.NewStaticObjectInfo("hello.txt", time.Now(), int64(len(data)), true, nil, f)); err != nil {
		t.Fatalf("upload through rclone: %v", err)
	}
	o, err := f.NewObject(ctx, "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := o.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("read back %q", got)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := davServer(t)
	w := testProvider()
	_, _, err := w.Login(context.Background(), provider.Env{}, provider.LoginOptions{
		Server: provider.Server{URL: srv.URL + "/dav"},
		Values: map[string]string{optUser: "alice", optPassword: "wrong"}})
	var ue *provider.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "비밀번호") {
		t.Fatalf("err = %v", err)
	}
	// later, a changed password means signing in again
	_, err = w.Drives(context.Background(), provider.Account{Server: srv.URL + "/dav", User: "alice"}, provider.Secret{Password: "old"})
	if !provider.NeedsSignIn(err) {
		t.Fatalf("drives with a stale password: %v", err)
	}
}

func TestResolveNotWebDAV(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer plain.Close()
	if _, err := testProvider().Resolve(context.Background(), plain.URL); err == nil {
		t.Fatal("a plain web server passed as WebDAV")
	}
}

// RFC 4331 quota properties become the drive's quota.
func TestQuotaProperties(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href>
<d:propstat><d:prop><d:quota-used-bytes>250</d:quota-used-bytes><d:quota-available-bytes>750</d:quota-available-bytes></d:prop>
<d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`))
	}))
	defer srv.Close()
	q, err := testProvider().quota(context.Background(), provider.Account{Server: srv.URL}, provider.Secret{Password: "x"})
	if err != nil || q == nil || q.Used != 250 || q.Total != 1000 {
		t.Fatalf("quota = %+v, %v", q, err)
	}
}
