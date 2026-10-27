// Package stream uploads a file to the server while it is being written,
// so a copy finishes when the server has the data ("instant" mode).
//
// Incoming writes land in a local shadow file at their offsets; a
// background loop sends the contiguous prefix to the server as tus PATCH
// requests, reading from the shadow as data arrives. Writers are held back
// when they run too far ahead of what the server has acknowledged, so the
// copy progress the user sees tracks the real upload. The shadow keeps
// everything, so the file can still be handed to the regular write cache
// if streaming has to be abandoned.
package stream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/tus"
)

// Defaults for Config.
const (
	DefaultWindow = 64 << 20
	DefaultTries  = 8 // about a minute of retries: rides out Wi-Fi coming back after sleep
	DefaultHold   = 64 << 20
)

// Config is shared by all uploads of a mount.
type Config struct {
	Client    *tus.Client
	ShadowDir string
	// Chunk caps the bytes per PATCH request. By default (0) a file goes
	// in one continuous request: every request boundary stalls the
	// stream while the server acknowledges it.
	Chunk  int64
	Window int64 // how far writes may run ahead of what is on the wire
	Tries  int   // attempts per PATCH before giving up
	// HoldMax is the largest file collected first and sent on close, so
	// it keeps the modification time set after writing.
	HoldMax int64
}

func (c *Config) defaults() {
	if c.Window <= 0 {
		c.Window = DefaultWindow
	}
	if c.HoldMax == 0 {
		c.HoldMax = DefaultHold
	}
	if c.Tries <= 0 {
		c.Tries = DefaultTries
	}
}

// patchLen is how much the next PATCH from off carries.
func (c *Config) patchLen(off, size int64) int64 {
	if c.Chunk > 0 {
		return min(c.Chunk, size-off)
	}
	return size - off
}

// Errors that make a handle fall back to the write cache.
var (
	ErrRewrite    = errors.New("stream: write into data already sent")
	ErrOutOfRange = errors.New("stream: write beyond the declared size")
	ErrAborted    = errors.New("stream: upload aborted")
)

// Upload is one file streaming to the server.
type Upload struct {
	cfg    Config
	coll   string // WebDAV folder URL
	name   string
	size   int64
	shadow *os.File
	path   string // shadow file path
	start  time.Time

	mu        sync.Mutex
	cond      *sync.Cond
	filled    spans     // byte ranges written so far
	inflight  []span    // writes copying into the shadow right now
	readPos   int64     // how far the sender has consumed the shadow
	sent      int64     // bytes the server acknowledged
	sentAt    time.Time // when the last byte went out; the server then finishes
	failed    error     // sending gave up; the shadow is still complete
	aborted   bool
	committed bool          // the server-side upload exists and sending runs
	done      chan struct{} // closed when sending ends, successfully or not
}

// Start creates the server-side upload and begins sending in the background.
// mtime, when set, is stored with the file.
func Start(ctx context.Context, cfg Config, collection, name string, size int64, mtime time.Time) (*Upload, error) {
	u, err := Prepare(cfg, collection, name, size)
	if err != nil {
		return nil, err
	}
	if err := u.Commit(ctx, mtime); err != nil {
		u.Close()
		return nil, err
	}
	return u, nil
}

// Prepare sets up the shadow without contacting the server; writes are
// collected until Commit. Small files use this so the upload can carry
// the modification time, which programs set only after writing and which
// OpenCloud can't change once a file is stored.
func Prepare(cfg Config, collection, name string, size int64) (*Upload, error) {
	cfg.defaults()
	if err := os.MkdirAll(cfg.ShadowDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(cfg.ShadowDir, "upload-*.part")
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	u := &Upload{cfg: cfg, coll: collection, name: name, size: size, shadow: f, path: f.Name(), start: time.Now(), done: make(chan struct{})}
	u.cond = sync.NewCond(&u.mu)
	register(u)
	return u, nil
}

// Commit creates the server-side upload, with mtime when set, and starts
// sending what has been and will be written.
func (u *Upload) Commit(ctx context.Context, mtime time.Time) error {
	meta := map[string]string{}
	if !mtime.IsZero() {
		meta["mtime"] = strconv.FormatInt(mtime.Unix(), 10)
	}
	target, err := u.cfg.Client.Create(ctx, u.coll, u.name, u.size, meta)
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.committed = true
	u.mu.Unlock()
	go u.send(target)
	return nil
}

// Committed reports whether sending has started.
func (u *Upload) Committed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.committed
}

// Name is the file name being uploaded.
func (u *Upload) Name() string { return u.name }

// Size is the declared file size.
func (u *Upload) Size() int64 { return u.size }

// WriteAt stores p at off, waiting while the writer is too far ahead.
func (u *Upload) WriteAt(p []byte, off int64) (int, error) {
	end := off + int64(len(p))
	if end > u.size {
		return 0, ErrOutOfRange
	}
	u.mu.Lock()
	// Judge by where the write starts: a single write larger than the
	// window must still get through, or the sender would wait for it forever.
	// Held files (not committed) are collected in full before sending.
	for u.committed && !u.aborted && u.failed == nil && off-u.readPos > u.cfg.Window {
		u.cond.Wait()
	}
	if u.aborted {
		u.mu.Unlock()
		return 0, ErrAborted
	}
	if off < u.readPos {
		u.mu.Unlock()
		return 0, ErrRewrite
	}
	w := span{off, end}
	u.inflight = append(u.inflight, w)
	u.mu.Unlock()

	n, err := u.shadow.WriteAt(p, off)
	u.mu.Lock()
	u.filled.add(off, off+int64(n))
	for i, x := range u.inflight {
		if x == w {
			u.inflight = append(u.inflight[:i], u.inflight[i+1:]...)
			break
		}
	}
	u.cond.Broadcast()
	u.mu.Unlock()
	return n, err
}

// Seal aborts sending and waits for writes in progress to land, so the
// shadow can be copied knowing nothing more will change it.
func (u *Upload) Seal() {
	u.mu.Lock()
	u.aborted = true
	u.cond.Broadcast()
	for len(u.inflight) > 0 {
		u.cond.Wait()
	}
	u.mu.Unlock()
}

// claimable is how far from pos the sender may read: written, and not
// overlapping a write still in progress. Call with mu held.
func (u *Upload) claimable(pos, end int64) int64 {
	limit := min(u.filled.prefix(), end)
	for _, w := range u.inflight {
		if w.to > pos && w.from < limit {
			limit = max(w.from, pos)
		}
	}
	return limit
}

// ReadAt reads back from the shadow.
func (u *Upload) ReadAt(p []byte, off int64) (int, error) {
	if off >= u.size {
		return 0, io.EOF
	}
	if max := u.size - off; int64(len(p)) > max {
		p = p[:max]
		n, err := u.shadow.ReadAt(p, off)
		if err == nil {
			err = io.EOF
		}
		return n, err
	}
	return u.shadow.ReadAt(p, off)
}

// Complete reports whether every byte has been written.
func (u *Upload) Complete() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.filled.prefix() >= u.size
}

// Status is how far the upload is, for display.
func (u *Upload) Status() Status {
	u.mu.Lock()
	defer u.mu.Unlock()
	st := Status{Name: u.name, Sent: u.readPos, Size: u.size}
	if s := time.Since(u.start).Seconds(); s > 0 {
		st.Speed = int64(float64(u.readPos) / s)
	}
	st.Waiting = u.readPos >= u.size && u.sent < u.size
	if st.Waiting && !u.sentAt.IsZero() {
		st.Left = finishLeft(u.size, u.sentAt)
	}
	return st
}

// Wait blocks until sending ends or ctx is done. It returns nil only
// when the server has the whole file.
func (u *Upload) Wait(ctx context.Context) error {
	select {
	case <-u.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.aborted:
		return ErrAborted
	case u.failed != nil:
		return u.failed
	}
	return nil
}

// Failed reports a send failure without waiting.
func (u *Upload) Failed() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.failed
}

// Abort stops sending; the server drops the unfinished upload when it expires.
func (u *Upload) Abort() {
	u.mu.Lock()
	u.aborted = true
	u.cond.Broadcast()
	u.mu.Unlock()
}

// Shadow exposes the local copy for handing over to the write cache.
func (u *Upload) Shadow() io.ReaderAt { return u.shadow }

// Close removes the shadow file. Call after sending ended or was aborted.
func (u *Upload) Close() error {
	unregister(u)
	u.shadow.Close()
	return os.Remove(u.path)
}

// send streams the file in Chunk-sized PATCH requests, resuming from the
// server's offset after transient failures.
func (u *Upload) send(target string) {
	defer close(u.done)
	ctx := context.Background()
	var off int64
	for off < u.size {
		n := u.cfg.patchLen(off, u.size)
		var err error
		for try := 1; ; try++ {
			if u.isAborted() {
				return
			}
			var got int64
			got, err = u.cfg.Client.Patch(ctx, target, off, &prefixReader{u: u, pos: off, end: off + n}, n)
			if err == nil {
				off = got
				break
			}
			if u.isAborted() || try >= u.cfg.Tries {
				break
			}
			slog.Warn(fmt.Sprintf("업로드 재시도 %s @%d (%d/%d): %v", u.name, off, try, u.cfg.Tries, err))
			u.mu.Lock()
			u.sentAt = time.Time{} // a resend would skew what the server's finish takes
			u.mu.Unlock()
			time.Sleep(retryDelay(try))
			if got, oerr := u.cfg.Client.Offset(ctx, target); oerr == nil {
				off = got
				n = u.cfg.patchLen(off, u.size)
			}
		}
		if err != nil {
			u.mu.Lock()
			if !u.aborted {
				u.failed = err
			}
			u.cond.Broadcast()
			u.mu.Unlock()
			if !u.isAborted() {
				slog.Error(fmt.Sprintf("업로드 실패 %s: %v", u.name, err))
			}
			return
		}
		u.mu.Lock()
		u.sent = off
		if off >= u.size && !u.sentAt.IsZero() {
			learnFinish(u.size, time.Since(u.sentAt))
		}
		u.cond.Broadcast()
		u.mu.Unlock()
	}
}

func (u *Upload) isAborted() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.aborted
}

// prefixReader feeds a PATCH body from the shadow, waiting for the bytes
// to be written.
type prefixReader struct {
	u        *Upload
	pos, end int64
}

func (r *prefixReader) Read(p []byte) (int, error) {
	if r.pos >= r.end {
		return 0, io.EOF
	}
	u := r.u
	u.mu.Lock()
	for !u.aborted && u.claimable(r.pos, r.end) <= r.pos {
		u.cond.Wait()
	}
	if u.aborted {
		u.mu.Unlock()
		return 0, ErrAborted
	}
	// Claim only what this read takes: readPos paces the writers, so it
	// must follow the network, not run to the end of what is written.
	avail := min(u.claimable(r.pos, r.end)-r.pos, int64(len(p)))
	if r.pos+avail > u.readPos {
		u.readPos = r.pos + avail // writes below this are no longer allowed
		if u.readPos == u.size && u.sentAt.IsZero() {
			u.sentAt = time.Now()
		}
		u.cond.Broadcast() // writers held by the window may go on
	}
	u.mu.Unlock()

	n, err := u.shadow.ReadAt(p[:avail], r.pos)
	r.pos += int64(n)
	if err == io.EOF && n > 0 {
		err = nil
	}
	return n, err
}

// ShadowDir is where a mount keeps in-flight upload files.
func ShadowDir(cacheDir string) string { return filepath.Join(cacheDir, "stream") }

// retryDelay backs off exponentially, capped at 15s.
func retryDelay(try int) time.Duration {
	return min(time.Second<<min(try-1, 4), 15*time.Second)
}
