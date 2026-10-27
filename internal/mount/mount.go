// Package mount mounts provider remotes as local drives using rclone's VFS,
// compiled into this binary so users need nothing besides the OS-level
// filesystem driver (WinFsp on Windows; macOS uses its built-in NFS client).
package mount

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/rclone/rclone/cmd/mountlib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/log"
	"github.com/rclone/rclone/fs/rc"
	"github.com/rclone/rclone/vfs/vfscommon"

	// The VFS write cache stores its files through the local backend;
	// without it rclone silently turns the cache off and writes that seek fail.
	_ "github.com/rclone/rclone/backend/local"
	// backends providers may ask for
	_ "github.com/rclone/rclone/backend/webdav"

	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/stream"
)

// Request describes one mount.
type Request struct {
	Name   string // unique name for logs and the rclone fs
	Remote provider.Remote
	// Target is a drive letter ("Z:") on Windows, a directory elsewhere.
	// Empty picks a platform default.
	Target string
	Label  string // volume name shown in the file manager
	// TotalSize, when positive, is reported as the drive's capacity; it
	// comes from the server's quota. Otherwise the backend decides.
	TotalSize int64
	// Instant, when set, makes copies finish only once the server has
	// the file (Windows only; ignored elsewhere).
	Instant *Instant
}

// Instant configures instant mode for a mount.
type Instant struct {
	Folder    string // the drive's WebDAV URL; tus uploads start from its folders
	Token     func(ctx context.Context) (string, error)
	ShadowDir string // where files being uploaded are kept meanwhile
}

// SupportsInstant reports whether this platform can mount in instant mode.
func SupportsInstant() bool { return supportsInstant }

// Mounted is a live mount.
type Mounted struct {
	Target string
	mp     *mountlib.MountPoint
}

// Uploads reports files written locally that are still being uploaded
// or waiting to be.
func (m *Mounted) Uploads() (inProgress, queued int) {
	if m.mp.VFS == nil {
		return 0, 0
	}
	dc, ok := m.mp.VFS.Stats()["diskCache"].(rc.Params)
	if !ok {
		return 0, 0
	}
	inProgress, _ = dc["uploadsInProgress"].(int)
	queued, _ = dc["uploadsQueued"].(int)
	return inProgress, queued
}

// Throughput is the current upload and download speed across all mounts,
// in bytes per second. rclone tracks every live transfer with a smoothed
// speed; uploads are the write cache copying to the server (they have a
// destination), downloads are reads streaming from it (they don't).
func Throughput() (up, down int64) { return Totals(Transfers()) }

// Transfer is one file moving between the PC and the server.
type Transfer struct {
	Name    string // path inside the drive
	Up      bool   // uploading to the server, otherwise reading from it
	Percent int
	Speed   int64 // smoothed, bytes per second
	Instant bool  // sent while being copied (instant mode), not from the write cache
}

// Transfers lists what all mounts are moving right now, sorted by name.
func Transfers() []Transfer {
	var out []Transfer
	if st, err := accounting.GlobalStats().RemoteStats(false); err == nil {
		list, _ := st["transferring"].([]rc.Params)
		out = transfersFrom(list)
	}
	// instant-mode uploads bypass rclone's accounting
	for _, s := range stream.Active() {
		pct := 0
		if s.Size > 0 {
			pct = int(s.Sent * 100 / s.Size)
		}
		out = append(out, Transfer{Name: s.Name, Up: true, Percent: pct, Speed: s.Speed, Instant: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Changes grows when files are deleted or instant uploads finish, i.e. when
// a drive's used space changed without going through rclone's uploads.
func Changes() int64 { return changes() }

// InstantUploads counts instant-mode files being sent to the server.
func InstantUploads() int { return len(stream.Active()) }

func transfersFrom(list []rc.Params) []Transfer {
	out := make([]Transfer, 0, len(list))
	for _, t := range list {
		tr := Transfer{}
		tr.Name, _ = t["name"].(string)
		_, tr.Up = t["dstFs"]
		tr.Percent, _ = t["percentage"].(int)
		if speed, ok := t["speedAvg"].(float64); ok {
			tr.Speed = int64(speed)
		}
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Totals sums upload and download speed.
func Totals(ts []Transfer) (up, down int64) {
	for _, t := range ts {
		if t.Up {
			up += t.Speed
		} else {
			down += t.Speed
		}
	}
	return up, down
}

// Wait blocks until the mount ends, unmounting on interrupt.
func (m *Mounted) Wait() error { return m.mp.Wait() }

// Unmount detaches the mount.
func (m *Mounted) Unmount() error { return m.mp.Unmount() }

// SetCacheDir points rclone's VFS write cache at dir.
func SetCacheDir(dir string) error { return config.SetCacheDir(dir) }

// LogToFile sends rclone's log to a rotating file; the desktop app has no
// console to print to.
// Info level records uploads and mounts, which is what problem reports need.
// The app's own events go through log/slog into the same file.
func LogToFile(path string) {
	fs.GetConfig(context.Background()).LogLevel = fs.LogLevelInfo
	log.Opt.File = path
	log.Opt.MaxSize = 5 * fs.Mebi
	log.Opt.MaxBackups = 3
	log.InitLogging()
	// InitLogging resets the level to rclone's start-up default (NOTICE),
	// which would drop uploads, mounts and the app's own events.
	log.Handler.SetLevel(fs.LogLevelToSlog(fs.LogLevelInfo))
}

// RcloneVersion is the embedded rclone's version, for the log header.
func RcloneVersion() string { return fs.Version }

// Start creates the backend and mounts it.
func Start(ctx context.Context, req Request) (*Mounted, error) {
	ri, err := fs.Find(req.Remote.Backend)
	if err != nil {
		return nil, err
	}
	// rclone waits a minute for a connection, per try; with the network gone
	// Explorer would hang that long on every folder. The backend's HTTP
	// client takes this when it is created.
	fs.GetConfig(ctx).ConnectTimeout = fs.Duration(10 * time.Second)
	f, err := ri.NewFs(ctx, req.Name, req.Remote.Root, configmap.Simple(req.Remote.Options))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", req.Name, err)
	}

	vfsOpt := vfscommon.Opt
	// Writes go through a temporary local file, uploaded on close and then
	// dropped; reads stream from the server.
	vfsOpt.CacheMode = vfscommon.CacheModeWrites
	// WebDAV can't push change notifications, so folder listings are only
	// as fresh as this cache. rclone's 5 minutes hides edits made on the
	// web or another PC; a listing costs one PROPFIND (~50 ms), so re-read
	// folders after a short while instead.
	vfsOpt.DirCacheTime = fs.Duration(10 * time.Second)
	vfsOpt.PollInterval = 0 // WebDAV has no change notifications; listings expire instead
	if req.TotalSize > 0 {
		vfsOpt.DiskSpaceTotalSize = fs.SizeSuffix(req.TotalSize)
	}

	mountOpt := mountlib.Opt
	mountOpt.VolumeName = req.Label

	if !supportsInstant {
		req.Instant = nil
	}
	fn, target, err := platform(&mountOpt, req)
	if err != nil {
		return nil, err
	}
	mp := mountlib.NewMountPoint(fn, target, f, &mountOpt, &vfsOpt)
	mp.Ctx = ctx
	if _, err := mp.Mount(); err != nil {
		return nil, explain(err)
	}
	return &Mounted{Target: mp.MountPoint, mp: mp}, nil
}

func explain(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "cgofuse") || strings.Contains(strings.ToLower(msg), "winfsp") {
		return fmt.Errorf("%w\nWinFsp is required: https://winfsp.dev/rel/", err)
	}
	return err
}
