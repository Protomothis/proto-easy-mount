package stream

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"

	_ "github.com/rclone/rclone/backend/local"

	"github.com/protomothis/proto-easy-mount/internal/tus"
	"github.com/protomothis/proto-easy-mount/internal/tus/tustest"
)

type testHost struct {
	v    *vfs.VFS
	path string
	coll string

	mu        sync.Mutex
	announced int64
	finished  int
	mtime     time.Time
}

func (h *testHost) Target() (string, string) { return h.coll, path.Base(h.path) }
func (h *testHost) OpenVFS(flags int) (vfs.Handle, error) {
	return h.v.OpenFile(h.path, flags, 0o644)
}
func (h *testHost) Announce(size int64) { h.mu.Lock(); h.announced = size; h.mu.Unlock() }
func (h *testHost) Finished(t time.Time) {
	h.mu.Lock()
	h.finished++
	h.mtime = t
	h.mu.Unlock()
}
func (h *testHost) Node() vfs.Node {
	if n, err := h.v.Stat(h.path); err == nil {
		return n
	}
	d, _ := h.v.Root()
	f, _ := d.Create(path.Base(h.path), 0)
	return f
}

type rig struct {
	fake *tustest.FakeServer
	cfg  Config
	host *testHost
}

func newRig(t *testing.T, fake *tustest.FakeServer) *rig {
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	config.SetCacheDir(t.TempDir())
	ri, err := fs.Find("local")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ri.NewFs(context.Background(), "local", t.TempDir(), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	opt := vfscommon.Opt
	opt.CacheMode = vfscommon.CacheModeWrites
	v := vfs.New(context.Background(), f, &opt)
	t.Cleanup(v.Shutdown)
	return &rig{
		fake: fake,
		cfg:  Config{Client: &tus.Client{HTTP: srv.Client()}, ShadowDir: t.TempDir(), Chunk: 1000, Window: 4000, Tries: 2, HoldMax: -1},
		host: &testHost{v: v, path: "file.bin", coll: srv.URL + "/dav/"},
	}
}

func (r *rig) vfsContent(t *testing.T) []byte {
	t.Helper()
	b, err := r.host.v.ReadFile(r.host.path)
	if err != nil {
		t.Fatalf("read back through VFS: %v", err)
	}
	return b
}

// Explorer's pattern: create, set size, write (a bit out of order), set
// the time, close. The server gets it, the cache never does.
func TestHandleStreamsExplorerCopy(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{})
	data := randomBytes(5000)
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	if err := h.Truncate(int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if !h.Streaming() || r.host.announced != 5000 {
		t.Fatalf("not streaming (announced %d)", r.host.announced)
	}
	for _, i := range []int{1, 0, 2, 4, 3} {
		if _, err := h.WriteAt(data[i*1000:(i+1)*1000], int64(i*1000)); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// setting the time mid-copy only records it
			h.SetModTime(time.Unix(1, 0))
			if r.host.finished != 0 || !h.Streaming() {
				t.Fatal("finished before all data was written")
			}
		}
	}
	if n := h.Node(); n.Size() != 5000 {
		t.Fatalf("node size %d during upload", n.Size())
	}
	mt := time.Date(2024, 3, 9, 16, 0, 0, 0, time.UTC)
	if err := h.SetModTime(mt); err != nil {
		t.Fatal(err)
	}
	// Explorer waits for this call (not for the close), so the server must
	// have everything once it returns
	got := r.fake.Uploads()["/dav/file.bin"]
	if got == nil || !got.Done() || !bytes.Equal(got.Data, data) {
		t.Fatal("server lacks the file after setting the time")
	}
	if r.host.finished != 1 || !r.host.mtime.Equal(mt) {
		t.Fatalf("finished=%d mtime=%v", r.host.finished, r.host.mtime)
	}
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.host.v.Stat(r.host.path); !errors.Is(err, vfs.ENOENT) {
		t.Fatalf("write cache got a copy too: %v", err)
	}
}

// Without the size up front there is nothing to stream; the cache takes it.
func TestHandleWriteWithoutSizeUsesCache(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{})
	data := randomBytes(3000)
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	if _, err := h.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if h.Streaming() {
		t.Fatal("streaming without a size")
	}
	h.Flush()
	h.Release()
	if !bytes.Equal(r.vfsContent(t), data) {
		t.Fatal("cache content differs")
	}
	if len(r.fake.Uploads()) != 0 {
		t.Fatal("tus upload started anyway")
	}
}

// Rewriting bytes the server already has can't be streamed; the handle
// moves everything to the cache and the result is still right.
func TestHandleFallsBackOnRewrite(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{})
	data := randomBytes(4000)
	h := NewHandle(r.host, r.cfg, os.O_RDWR|os.O_CREATE, true)
	h.Truncate(4000)
	h.WriteAt(data, 0)
	deadline := time.Now().Add(5 * time.Second)
	for h.up != nil && h.up.Status().Sent == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	patch := []byte("PATCHED")
	if _, err := h.WriteAt(patch, 100); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if h.Streaming() {
		t.Fatal("still streaming after a rewrite")
	}
	copy(data[100:], patch)
	h.Flush()
	h.Release()
	if !bytes.Equal(r.vfsContent(t), data) {
		t.Fatal("cache content differs after fallback")
	}
}

// If the server keeps failing, closing hands the file to the cache
// instead of losing it.
func TestHandleFallsBackOnServerFailure(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{FailPatch: func(int64) bool { return true }})
	data := randomBytes(3000)
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	h.Truncate(3000)
	if _, err := h.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := h.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	h.Release()
	if !bytes.Equal(r.vfsContent(t), data) {
		t.Fatal("file lost after server failure")
	}
	if r.host.finished != 0 {
		t.Fatal("reported finished despite failure")
	}
}

// Deleting the file mid-copy (Explorer cancel) leaves nothing behind.
func TestHandleDrop(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{})
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	h.Truncate(5000)
	h.WriteAt(randomBytes(1000), 0)
	h.Drop()
	if err := h.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.host.v.Stat(r.host.path); !errors.Is(err, vfs.ENOENT) {
		t.Fatalf("dropped file exists: %v", err)
	}
	for _, u := range r.fake.Uploads() {
		if u.Done() {
			t.Fatal("dropped upload completed")
		}
	}
}

// A created-and-closed empty file still has to exist.
func TestHandleEmptyFile(t *testing.T) {
	r := newRig(t, &tustest.FakeServer{})
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}
	h.Release()
	if n, err := r.host.v.Stat(r.host.path); err != nil || n.Size() != 0 {
		t.Fatalf("empty file: %v %v", n, err)
	}
}

// Small files are held until close so the upload carries the time
// Explorer sets after writing.
func TestHandleHoldsSmallFileForModTime(t *testing.T) {
	fake := &tustest.FakeServer{}
	r := newRig(t, fake)
	r.cfg.HoldMax = 1 << 20
	data := randomBytes(5000)
	h := NewHandle(r.host, r.cfg, os.O_WRONLY|os.O_CREATE, true)
	h.Truncate(5000)
	h.WriteAt(data, 0)
	if len(fake.Uploads()) != 0 {
		t.Fatal("small file sent before close")
	}
	mt := time.Date(2024, 3, 9, 16, 0, 0, 0, time.UTC)
	if err := h.SetModTime(mt); err != nil {
		t.Fatal(err)
	}
	got := fake.Uploads()["/dav/file.bin"]
	if got == nil || !bytes.Equal(got.Data, data) || got.Meta["mtime"] != "1710000000" {
		t.Fatalf("upload = %+v", got)
	}
	h.Flush()
	h.Release()
}
