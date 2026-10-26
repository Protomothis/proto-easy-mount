//go:build windows && cmount

package winfs

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	fspkg "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/vfs"

	"github.com/protomothis/proto-easy-mount/internal/stream"
)

// Streaming turns on instant mode: files written from the start with
// their size set first (Explorer copies) go straight to the server.
type Streaming struct {
	Config stream.Config
	// Folder returns the WebDAV URL, ending in "/", of a folder inside the
	// drive given as a slash-separated path ("" for the root).
	Folder func(dir string) string
}

// FolderURL builds Streaming.Folder for a drive whose root is base.
func FolderURL(base string) func(dir string) string {
	base = strings.TrimRight(base, "/")
	return func(dir string) string {
		u := base
		for _, seg := range strings.Split(strings.Trim(dir, "/"), "/") {
			if seg != "" {
				u += "/" + url.PathEscape(seg)
			}
		}
		return u + "/"
	}
}

// changes counts deletions and finished instant uploads.
var changes atomic.Int64

// Changes grows whenever a file operation changes the space a drive uses,
// so callers know when to read the quota again.
func Changes() int64 { return changes.Load() }

// fileHost connects one stream.Handle to the VFS around it.
type fileHost struct {
	fsys   *FS
	path   string // FUSE path, "/dir/name"
	parent *vfs.Dir
	leaf   string
	file   *vfs.File
}

func (h *fileHost) remote() string { return strings.TrimPrefix(h.path, "/") }

func (h *fileHost) Target() (string, string) {
	return h.fsys.stream.Folder(path.Dir(h.remote())), h.leaf
}

func (h *fileHost) OpenVFS(flags int) (vfs.Handle, error) {
	if h.file != nil {
		return h.file.Open(flags)
	}
	return h.fsys.VFS.OpenFile(h.path, flags, 0o777)
}

func (h *fileHost) Announce(size int64) { h.parent.AddVirtual(h.leaf, size, false) }

func (h *fileHost) Finished(mtime time.Time) {
	changes.Add(1)
	// Re-read the folder now, so the real file replaces the placeholder
	// before the program that wrote it (or a virus scanner) opens it again.
	h.parent.ForgetPath(h.leaf, fspkg.EntryObject)
	if _, err := h.parent.ReadDirAll(); err != nil {
		slog.Warn(fmt.Sprintf("업로드 후 목록을 다시 읽지 못함 %s: %v", h.remote(), err))
	}
	if mtime.IsZero() {
		return
	}
	ctx := context.Background()
	o, err := h.fsys.f.NewObject(ctx, h.remote())
	if err == nil {
		err = o.SetModTime(ctx, mtime)
	}
	if err != nil {
		slog.Warn(fmt.Sprintf("수정 시각을 저장하지 못함 %s: %v", h.remote(), err))
	}
}

func (h *fileHost) Node() vfs.Node {
	if n, err := h.fsys.VFS.Stat(h.path); err == nil {
		return n
	}
	return h.file
}

// newWriter wraps a file opened for writing and tracks it by path.
func (fsys *FS) newWriter(p string, parent *vfs.Dir, leaf string, file *vfs.File, flags int, fresh bool) *stream.Handle {
	host := &fileHost{fsys: fsys, path: p, parent: parent, leaf: leaf, file: file}
	h := stream.NewHandle(host, fsys.stream.Config, flags, fresh)
	fsys.mu.Lock()
	fsys.writing[p] = h
	fsys.mu.Unlock()
	return h
}

// openForWrite gives existing files opened for writing a stream handle in
// instant mode; it returns nil to let the regular path handle the open.
func (fsys *FS) openForWrite(p string, flags int) (vfs.Handle, int) {
	if fsys.stream == nil || flags&(os.O_WRONLY|os.O_RDWR) == 0 {
		return nil, 0
	}
	node, err := fsys.VFS.Stat(p)
	if err != nil {
		return nil, 0
	}
	file, ok := node.(*vfs.File)
	if !ok {
		return nil, 0
	}
	leaf, parent, errc := fsys.lookupParentDir(p)
	if errc != 0 {
		return nil, errc
	}
	return fsys.newWriter(p, parent, leaf, file, flags, false), 0
}

// openWhileWriting lets programs read a file that is still uploading
// (Explorer previews, virus scanners). The VFS only has a placeholder
// with no data then, so reads come from what has been written so far.
func (fsys *FS) openWhileWriting(p string, flags int) vfs.Handle {
	if flags&(os.O_WRONLY|os.O_RDWR) != 0 {
		return nil
	}
	h := fsys.writer(p)
	if h == nil || !h.Streaming() {
		return nil
	}
	return readView{h}
}

// readView is a read-only handle onto a file being written.
type readView struct{ *stream.Handle }

func (r readView) WriteAt([]byte, int64) (int, error) { return 0, vfs.EBADF }
func (r readView) Truncate(int64) error               { return vfs.EBADF }
func (r readView) Flush() error                       { return nil }
func (r readView) Release() error                     { return nil }
func (r readView) Close() error                       { return nil }
func (r readView) SetModTime(time.Time) error         { return nil }

// writer returns the instant-mode handle writing p, if any.
func (fsys *FS) writer(p string) *stream.Handle {
	fsys.mu.Lock()
	defer fsys.mu.Unlock()
	return fsys.writing[p]
}

func (fsys *FS) forgetWriter(p string, h *stream.Handle) {
	fsys.mu.Lock()
	if fsys.writing[p] == h {
		delete(fsys.writing, p)
	}
	fsys.mu.Unlock()
}
