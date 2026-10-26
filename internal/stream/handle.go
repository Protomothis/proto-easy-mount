package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/rclone/rclone/vfs"
)

// Host is what a Handle needs from the filesystem around it.
type Host interface {
	// Target names the upload: the WebDAV folder URL and the file name.
	Target() (collection, leaf string)
	// OpenVFS opens the file through the regular VFS write cache.
	OpenVFS(flags int) (vfs.Handle, error)
	// Announce shows the file, at its final size, in listings while it uploads.
	Announce(size int64)
	// Finished tells the host the server has the file: refresh listings
	// and, when mtime is set, store it on the server.
	Finished(mtime time.Time)
	// Node is the VFS node for this path (a placeholder while uploading).
	Node() vfs.Node
}

type state int

const (
	pending   state = iota // opened, nothing decided yet
	streaming              // sending to the server as data arrives
	cached                 // handed to the VFS write cache
	finished               // the server has it
	dropped                // deleted while uploading
)

// flushWait caps how long closing a file waits for the server, so the
// filesystem request can't hit the OS timeout; the rest finishes in the
// background.
var flushWait = 4 * time.Minute

// Handle is a vfs.Handle that streams a freshly written file to the server
// and falls back to the VFS write cache whenever the writes don't fit
// streaming (no size up front, rewrites, failures).
type Handle struct {
	host  Host
	cfg   Config
	flags int  // flags the file was opened with
	fresh bool // content starts empty: new file, O_TRUNC or truncated to 0

	mu     sync.Mutex
	st     state
	truncs []int64 // truncations seen while pending, replayed on fallback
	up     *Upload
	vh     vfs.Handle
	size   int64
	mtime  time.Time
}

// NewHandle wraps a file opened for writing.
func NewHandle(host Host, cfg Config, flags int, fresh bool) *Handle {
	cfg.defaults()
	return &Handle{host: host, cfg: cfg, flags: flags, fresh: fresh || flags&os.O_TRUNC != 0}
}

// Streaming reports whether the file is currently being sent directly.
func (h *Handle) Streaming() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st == streaming
}

// Truncate is how Explorer announces the final size before copying.
func (h *Handle) Truncate(size int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch h.st {
	case pending:
		if size == 0 {
			h.fresh = true
			h.truncs = append(h.truncs, 0)
			return nil
		}
		if h.fresh {
			coll, leaf := h.host.Target()
			var up *Upload
			var err error
			if size <= h.cfg.HoldMax {
				// small: collect, then send on close with the file's time
				up, err = Prepare(h.cfg, coll, leaf, size)
			} else {
				// large: send while writing; OpenCloud then keeps the upload time
				up, err = Start(context.Background(), h.cfg, coll, leaf, size, h.mtime)
			}
			if err == nil {
				h.up, h.size, h.st = up, size, streaming
				h.host.Announce(size)
				slog.Info(fmt.Sprintf("즉시 업로드 시작 %s (%d bytes, 바로 전송=%v)", leaf, size, up.Committed()))
				return nil
			}
			slog.Warn(fmt.Sprintf("즉시 업로드를 시작하지 못해 캐시로 처리 %s: %v", leaf, err))
		}
		h.truncs = append(h.truncs, size)
		if err := h.toCache(); err != nil {
			return err
		}
		return nil
	case streaming:
		if size == h.size {
			return nil
		}
		if err := h.fallback("크기 변경"); err != nil {
			return err
		}
	case finished, dropped:
		return vfs.EBADF
	}
	return h.vh.Truncate(size)
}

// WriteAt sends streaming writes to the upload, others to the cache.
func (h *Handle) WriteAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	switch h.st {
	case streaming:
		up := h.up
		h.mu.Unlock()
		// may wait for the server; don't hold the handle lock meanwhile
		n, err := up.WriteAt(p, off)
		if err == nil || !(errors.Is(err, ErrRewrite) || errors.Is(err, ErrOutOfRange) || errors.Is(err, ErrAborted)) {
			return n, err
		}
		h.mu.Lock()
		if h.st == streaming && h.up == up {
			if ferr := h.fallback(err.Error()); ferr != nil {
				h.mu.Unlock()
				return 0, ferr
			}
		}
		if h.st != cached {
			h.mu.Unlock()
			return 0, vfs.EBADF
		}
	case pending:
		if err := h.toCache(); err != nil {
			h.mu.Unlock()
			return 0, err
		}
	case finished, dropped:
		h.mu.Unlock()
		return 0, vfs.EBADF
	}
	vh := h.vh
	h.mu.Unlock()
	return vh.WriteAt(p, off)
}

// ReadAt reads back what was written.
func (h *Handle) ReadAt(p []byte, off int64) (int, error) {
	h.mu.Lock()
	switch h.st {
	case streaming:
		up := h.up
		h.mu.Unlock()
		return up.ReadAt(p, off)
	case pending:
		if h.fresh {
			h.mu.Unlock()
			return 0, io.EOF
		}
		if err := h.toCache(); err != nil {
			h.mu.Unlock()
			return 0, err
		}
	case finished, dropped:
		h.mu.Unlock()
		return 0, vfs.EBADF
	}
	vh := h.vh
	h.mu.Unlock()
	return vh.ReadAt(p, off)
}

// Flush runs when the program closes the file. For a fully written
// stream it waits until the server has the file. (WinFsp 2.1 flushes only
// once the file is fully closed, which programs don't wait for; SetModTime
// is where a copy actually waits.)
func (h *Handle) Flush() error {
	h.mu.Lock()
	switch h.st {
	case streaming:
		if !h.up.Complete() {
			h.mu.Unlock()
			return nil // closed early; Release decides
		}
		moved, err := h.settle()
		if err != nil || !moved {
			return err
		}
		h.mu.Lock()
	case pending:
		if h.fresh {
			// created or emptied but never written: the cache makes the empty file
			if err := h.toCache(); err != nil {
				h.mu.Unlock()
				return err
			}
			break
		}
		h.mu.Unlock()
		return nil
	case finished, dropped:
		h.mu.Unlock()
		return nil
	}
	vh := h.vh
	h.mu.Unlock()
	return vh.Flush()
}

// Release runs after the last close.
func (h *Handle) Release() error {
	h.mu.Lock()
	switch h.st {
	case streaming:
		up := h.up
		complete := up.Complete()
		var err error
		if complete {
			err = h.commit()
		}
		h.mu.Unlock()
		if complete && err == nil {
			err = up.Wait(context.Background())
		}
		h.mu.Lock()
		if h.st != streaming || h.up != up {
			break
		}
		if complete && err == nil {
			h.finish()
			h.mu.Unlock()
			return nil
		}
		reason := "닫을 때 일부만 쓰여 있음"
		if err != nil {
			reason = err.Error()
		}
		if ferr := h.fallback(reason); ferr != nil {
			h.mu.Unlock()
			return ferr
		}
	case pending:
		if !h.fresh {
			h.mu.Unlock()
			return nil
		}
		if err := h.toCache(); err != nil {
			h.mu.Unlock()
			return err
		}
	case finished, dropped:
		h.mu.Unlock()
		return nil
	}
	vh := h.vh
	h.mu.Unlock()
	if vh == nil {
		return nil
	}
	return vh.Release()
}

// Drop abandons the upload because the file was deleted.
func (h *Handle) Drop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st == streaming {
		h.up.Seal()
		h.up.Close()
		h.up = nil
	}
	if h.st == streaming || h.st == pending {
		h.st = dropped
	}
}

// SetModTime records the modification time Explorer sets near the end of
// a copy; it is stored on the server once the upload completes.
//
// Setting the time after the last write is the final step of a copy that
// the copying program waits on (closing isn't), so a fully written stream
// is finished here: the copy ends when the server has the file.
func (h *Handle) SetModTime(t time.Time) error {
	h.mu.Lock()
	h.mtime = t
	switch h.st {
	case cached:
		defer h.mu.Unlock()
		return h.vh.Node().SetModTime(t)
	case streaming:
		if h.up.Complete() {
			_, err := h.settle() // a move to the cache already took the time
			return err
		}
	}
	h.mu.Unlock()
	return nil
}

// settle commits a fully written stream, waits for the server and then
// finishes it, or moves it to the write cache if the server failed.
// moved reports the move. Call with mu held; it returns with mu released.
func (h *Handle) settle() (moved bool, err error) {
	up := h.up
	if err := h.commit(); err != nil {
		defer h.mu.Unlock()
		if ferr := h.fallback(err.Error()); ferr != nil {
			return false, ferr
		}
		return true, nil
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), flushWait)
	err = up.Wait(ctx)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return false, nil // keeps going; Release finishes it
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.up != up || h.st != streaming {
		return false, nil // settled by another call meanwhile
	}
	if err == nil {
		h.finish()
		return false, nil
	}
	if ferr := h.fallback(err.Error()); ferr != nil {
		return false, ferr
	}
	return true, nil
}

// commit starts sending a held-back small file, now that its time is
// known. Call with mu held.
func (h *Handle) commit() error {
	if h.up.Committed() {
		return nil
	}
	return h.up.Commit(context.Background(), h.mtime)
}

// toCache opens the VFS handle with the original intent and replays
// truncations. Call with mu held.
func (h *Handle) toCache() error {
	flags := h.flags
	if h.fresh {
		flags |= os.O_CREATE | os.O_TRUNC
	}
	vh, err := h.host.OpenVFS(flags)
	if err != nil {
		return err
	}
	for _, size := range h.truncs {
		if err := vh.Truncate(size); err != nil {
			vh.Release()
			return err
		}
	}
	h.vh, h.st = vh, cached
	if !h.mtime.IsZero() {
		vh.Node().SetModTime(h.mtime)
	}
	return nil
}

// fallback stops streaming and moves everything written so far into the
// VFS write cache, which uploads it the regular way. Call with mu held.
func (h *Handle) fallback(reason string) error {
	up := h.up
	up.Seal()
	_, leaf := h.host.Target()
	slog.Warn(fmt.Sprintf("즉시 업로드 → 캐시로 전환 %s: %s", leaf, reason))
	vh, err := h.host.OpenVFS(os.O_RDWR | os.O_CREATE | os.O_TRUNC)
	if err != nil {
		up.Close()
		return err
	}
	buf := make([]byte, 1<<20)
	for off := int64(0); off < h.size; {
		n, rerr := up.Shadow().ReadAt(buf[:min(int64(len(buf)), h.size-off)], off)
		if n > 0 {
			if _, werr := vh.WriteAt(buf[:n], off); werr != nil {
				up.Close()
				vh.Release()
				return werr
			}
			off += int64(n)
		}
		if rerr != nil && rerr != io.EOF {
			up.Close()
			vh.Release()
			return rerr
		}
		if n == 0 {
			break
		}
	}
	up.Close()
	h.up, h.vh, h.st = nil, vh, cached
	if !h.mtime.IsZero() {
		vh.Node().SetModTime(h.mtime)
	}
	return nil
}

// finish wraps up a successful upload. Call with mu held.
func (h *Handle) finish() {
	_, leaf := h.host.Target()
	st := h.up.Status()
	h.up.Close()
	h.up, h.st = nil, finished
	h.host.Finished(h.mtime)
	slog.Info(fmt.Sprintf("즉시 업로드 완료 %s (%d bytes)", leaf, st.Size))
}

// Node reports the file with the size being uploaded.
func (h *Handle) Node() vfs.Node {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st == cached {
		return h.vh.Node()
	}
	n := h.host.Node()
	if h.st == streaming || h.st == finished {
		return sizedNode{Node: n, size: h.size, mtime: h.mtime}
	}
	return n
}

// sizedNode overrides size and time of a placeholder node.
type sizedNode struct {
	vfs.Node
	size  int64
	mtime time.Time
}

func (n sizedNode) Size() int64 { return n.size }

func (n sizedNode) ModTime() time.Time {
	if n.mtime.IsZero() {
		return n.Node.ModTime()
	}
	return n.mtime
}

// The rest of vfs.Handle: what FUSE doesn't use reports "not supported".

func (h *Handle) Stat() (os.FileInfo, error)         { return h.Node(), nil }
func (h *Handle) Name() string                       { return h.Node().Path() }
func (h *Handle) Close() error                       { return h.Release() }
func (h *Handle) Sync() error                        { return nil }
func (h *Handle) Lock() error                        { return nil }
func (h *Handle) Unlock() error                      { return nil }
func (h *Handle) Chdir() error                       { return vfs.ENOSYS }
func (h *Handle) Chmod(os.FileMode) error            { return vfs.ENOSYS }
func (h *Handle) Chown(int, int) error               { return vfs.ENOSYS }
func (h *Handle) Fd() uintptr                        { return 0 }
func (h *Handle) Read([]byte) (int, error)           { return 0, vfs.ENOSYS }
func (h *Handle) Readdir(int) ([]os.FileInfo, error) { return nil, vfs.ENOSYS }
func (h *Handle) Readdirnames(int) ([]string, error) { return nil, vfs.ENOSYS }
func (h *Handle) Seek(int64, int) (int64, error)     { return 0, vfs.ENOSYS }
func (h *Handle) Write([]byte) (int, error)          { return 0, vfs.ENOSYS }
func (h *Handle) WriteString(string) (int, error)    { return 0, vfs.ENOSYS }

var _ vfs.Handle = (*Handle)(nil)
