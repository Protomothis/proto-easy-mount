//go:build windows && cmount

package winfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/cmd/mountlib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"
	"github.com/winfsp/cgofuse/fuse"

	_ "github.com/rclone/rclone/backend/local"
	_ "github.com/rclone/rclone/backend/webdav"

	"github.com/protomothis/proto-easy-mount/internal/stream"
	"github.com/protomothis/proto-easy-mount/internal/tus"
	"github.com/protomothis/proto-easy-mount/internal/tus/tustest"
)

func newVFS(t *testing.T, backend string, opts configmap.Simple) *vfs.VFS {
	config.SetCacheDir(t.TempDir())
	ri, err := fs.Find(backend)
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	if backend == "local" {
		root = t.TempDir()
	}
	f, err := ri.NewFs(context.Background(), backend, root, opts)
	if err != nil {
		t.Fatal(err)
	}
	o := vfscommon.Opt
	o.CacheMode = vfscommon.CacheModeWrites
	v := vfs.New(context.Background(), f, &o)
	t.Cleanup(v.Shutdown)
	return v
}

// explorerCopy replays the calls WinFsp makes when Explorer copies a file
// onto the drive, returning how long the closing Flush took.
func explorerCopy(t *testing.T, fsys *FS, p string, data []byte, mtime time.Time) {
	t.Helper()
	fi := fuse.FileInfo_t{Flags: fuse.O_WRONLY | fuse.O_CREAT}
	if errc := fsys.CreateEx(p, 0o644, &fi); errc != 0 {
		t.Fatalf("create: %d", errc)
	}
	if errc := fsys.Truncate(p, int64(len(data)), fi.Fh); errc != 0 {
		t.Fatalf("truncate: %d", errc)
	}
	block := len(data) / 4
	for _, i := range []int{1, 0, 3, 2} { // overlapped writes land out of order
		end := (i + 1) * block
		if i == 3 {
			end = len(data)
		}
		if n := fsys.Write(p, data[i*block:end], int64(i*block), fi.Fh); n != end-i*block {
			t.Fatalf("write %d: %d", i, n)
		}
	}
	var st fuse.Stat_t
	if errc := fsys.Getattr(p, &st, fhUnset); errc != 0 || st.Size != int64(len(data)) {
		t.Fatalf("getattr while uploading: errc=%d size=%d", errc, st.Size)
	}
	ts := fuse.NewTimespec(mtime)
	if errc := fsys.Utimens(p, []fuse.Timespec{ts, ts}); errc != 0 {
		t.Fatalf("utimens: %d", errc)
	}
	if errc := fsys.Flush(p, fi.Fh); errc != 0 {
		t.Fatalf("flush: %d", errc)
	}
	if errc := fsys.Release(p, fi.Fh); errc != 0 {
		t.Fatalf("release: %d", errc)
	}
}

func TestInstantCopyWithFakeServer(t *testing.T) {
	fake := &tustest.FakeServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	v := newVFS(t, "local", configmap.Simple{})
	fsys := NewFS(v, &mountlib.Options{}, &Streaming{
		Config: stream.Config{Client: &tus.Client{HTTP: srv.Client()}, ShadowDir: t.TempDir(), Chunk: 4096, Window: 8192},
		Folder: FolderURL(srv.URL + "/dav/spaces/x"),
	})
	data := make([]byte, 20000)
	rand.Read(data)
	if errc := fsys.Mkdir("/sub dir", 0o755); errc != 0 {
		t.Fatalf("mkdir: %d", errc)
	}
	explorerCopy(t, fsys, "/sub dir/가나.bin", data, time.Now())

	got := fake.Uploads()["/dav/spaces/x/sub dir/가나.bin"] // URL-decoded by the server
	if got == nil {
		for k := range fake.Uploads() {
			t.Logf("upload key %q", k)
		}
		t.Fatal("no upload at the expected folder")
	}
	if !got.Done() || !bytes.Equal(got.Data, data) {
		t.Fatal("server data differs")
	}
	if fsys.writer("/sub dir/가나.bin") != nil {
		t.Fatal("writer still tracked after release")
	}
}

// Stable mode (no streaming) must keep rclone's original behaviour.
func TestStableModeUnchanged(t *testing.T) {
	v := newVFS(t, "local", configmap.Simple{})
	fsys := NewFS(v, &mountlib.Options{}, nil)
	fi := fuse.FileInfo_t{Flags: fuse.O_WRONLY | fuse.O_CREAT}
	if errc := fsys.CreateEx("/a.txt", 0o644, &fi); errc != 0 {
		t.Fatal(errc)
	}
	if _, ok := fsys.handles[fi.Fh].(*stream.Handle); ok {
		t.Fatal("stream handle used in stable mode")
	}
	fsys.Write("/a.txt", []byte("hello"), 0, fi.Fh)
	fsys.Flush("/a.txt", fi.Fh)
	fsys.Release("/a.txt", fi.Fh)
	if b, err := v.ReadFile("a.txt"); err != nil || string(b) != "hello" {
		t.Fatalf("read back %q %v", b, err)
	}
}

// Against a real OpenCloud space: Explorer-style copies, then check the
// server already has the bytes when Flush returns. A small file must also
// keep its modification time; a large one streams and gets the upload
// time (OpenCloud can't change it afterwards). PEM_TOKEN and PEM_SPACE
// (the space's WebDAV URL) enable it.
func TestInstantCopyLive(t *testing.T) {
	tok, space := os.Getenv("PEM_TOKEN"), os.Getenv("PEM_SPACE")
	if tok == "" {
		t.Skip("set PEM_TOKEN and PEM_SPACE")
	}
	token := func(context.Context) (string, error) { return tok, nil }
	v := newVFS(t, "webdav", configmap.Simple{"url": space, "vendor": "infinitescale", "bearer_token": tok})
	fsys := NewFS(v, &mountlib.Options{}, &Streaming{
		Config: stream.Config{Client: &tus.Client{Token: token}, ShadowDir: t.TempDir()},
		Folder: FolderURL(space),
	})
	if errc := fsys.Mkdir("/_pem_instant", 0o755); errc != 0 && errc != -fuse.EEXIST {
		t.Fatalf("mkdir: %d", errc)
	}
	defer fsys.Rmdir("/_pem_instant")
	mtime := time.Date(2024, 3, 9, 16, 0, 0, 0, time.UTC)

	for _, c := range []struct {
		name      string
		size      int
		keepsTime bool
	}{{"작은 문서.bin", 8 << 20, true}, {"큰 영상.bin", 160 << 20, false}} {
		data := make([]byte, c.size)
		rand.Read(data)
		p := "/_pem_instant/" + c.name
		start := time.Now()
		explorerCopy(t, fsys, p, data, mtime)
		el := time.Since(start)
		t.Logf("%s: %d MB copy returned after %v (%.1f MB/s)", c.name, c.size>>20, el.Round(time.Millisecond),
			float64(c.size)/el.Seconds()/1e6)

		req, _ := http.NewRequest("GET", strings.TrimRight(space, "/")+"/_pem_instant/"+urlSeg(c.name), nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Equal(body, data) {
			t.Fatalf("%s on server: HTTP %d, %d bytes, equal=%v", c.name, resp.StatusCode, len(body), bytes.Equal(body, data))
		}
		lm, _ := time.Parse(time.RFC1123Z, resp.Header.Get("Last-Modified"))
		t.Logf("%s: server mtime %v", c.name, lm)
		if c.keepsTime && !lm.Equal(mtime) {
			t.Errorf("%s: mtime %v, want %v", c.name, lm, mtime)
		}
		fsys.Unlink(p)
	}
}

func urlSeg(s string) string { return strings.TrimSuffix(FolderURL("")(s), "/")[1:] }
