//go:build darwin

package mount

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/rclone/rclone/cmd/mountlib"

	// rclone serves NFS locally and macOS mounts it with its own client,
	// so no macFUSE kernel extension is needed.
	_ "github.com/rclone/rclone/cmd/nfsmount"
)

// Instant mode needs the Windows filesystem layer; macOS uses stable mode.
const supportsInstant = false

func platform(opt *mountlib.Options, req Request) (mountlib.MountFn, string, error) {
	target := req.Target
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", err
		}
		target = filepath.Join(home, "ProtoEasyMount", req.Label)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return nil, "", err
	}
	_, fn := mountlib.ResolveMountMethod("nfsmount")
	if fn == nil {
		return nil, "", errors.New("nfsmount not compiled into this build")
	}
	return fn, target, nil
}

// Changes made through the NFS server are not counted; the quota is
// re-read periodically instead.
func changes() int64 { return 0 }
