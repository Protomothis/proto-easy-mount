package mount

import (
	"context"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"
)

// Start copies rclone's global VFS options; they must carry real defaults
// even though no rclone command line was parsed.
func TestVFSDefaultsLoaded(t *testing.T) {
	if vfscommon.Opt.DirCacheTime <= 0 || vfscommon.Opt.CacheMaxAge <= 0 {
		t.Fatalf("VFS defaults missing: %+v", vfscommon.Opt)
	}
}

func TestTransfersAndTotals(t *testing.T) {
	ts := transfersFrom([]rc.Params{
		{"name": "b.zip", "srcFs": "local{cache}:", "dstFs": "webdav:", "speedAvg": 10e6, "percentage": 80},
		{"name": "a.zip", "srcFs": "local{cache}:", "dstFs": "webdav:", "speedAvg": 30e6, "percentage": 45},
		{"name": "movie.mkv", "srcFs": "webdav:", "speedAvg": 5e6},
		{"name": "stalled", "srcFs": "webdav:"}, // no speed yet
	})
	if len(ts) != 4 || ts[0].Name != "a.zip" || !ts[0].Up || ts[0].Percent != 45 || ts[2].Up {
		t.Fatalf("transfers = %+v", ts)
	}
	if up, down := Totals(ts); up != 40e6 || down != 5e6 {
		t.Fatalf("up=%d down=%d", up, down)
	}
}

// The write cache must actually start. rclone disables it with only a log
// line when its storage backend is missing, and then every write that
// seeks (Explorer copies do) fails and retries.
func TestWriteCacheStarts(t *testing.T) {
	if err := SetCacheDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	ri, err := fs.Find("local")
	if err != nil {
		t.Fatal(err)
	}
	f, err := ri.NewFs(context.Background(), "cachetest", t.TempDir(), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	opt := vfscommon.Opt
	opt.CacheMode = vfscommon.CacheModeWrites
	v := vfs.New(context.Background(), f, &opt)
	defer v.Shutdown()
	if v.Opt.CacheMode != vfscommon.CacheModeWrites {
		t.Fatalf("write cache was disabled (mode %v)", v.Opt.CacheMode)
	}

	// a seek-and-write, like Explorer's copy engine does
	h, err := v.OpenFile("probe.bin", 0x2|0x40, 0o644) // O_RDWR|O_CREATE
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.WriteAt([]byte("tail"), 1<<20); err != nil {
		t.Fatalf("write at offset: %v", err)
	}
	if _, err := h.WriteAt([]byte("head"), 0); err != nil {
		t.Fatalf("write back at start: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}
