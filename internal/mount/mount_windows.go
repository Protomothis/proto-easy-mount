//go:build windows && cmount

package mount

import (
	"github.com/rclone/rclone/cmd/mountlib"

	"github.com/protomothis/proto-easy-mount/internal/stream"
	"github.com/protomothis/proto-easy-mount/internal/tus"
	"github.com/protomothis/proto-easy-mount/internal/winfs"
)

const supportsInstant = true

func platform(opt *mountlib.Options, req Request) (mountlib.MountFn, string, error) {
	// Network mode makes Explorer treat it like a mapped network drive.
	opt.NetworkMode = true
	target := req.Target
	if target == "" {
		target = "*" // next free drive letter, from Z: backwards
	}
	var st *winfs.Streaming
	if in := req.Instant; in != nil {
		st = &winfs.Streaming{
			Config: stream.Config{Client: &tus.Client{Token: in.Token}, ShadowDir: in.ShadowDir},
			Folder: winfs.FolderURL(in.Folder),
		}
	}
	return winfs.Mount(st), target, nil
}

func changes() int64 { return winfs.Changes() }
