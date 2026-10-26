//go:build !windows && !darwin

package mount

import (
	"errors"

	"github.com/rclone/rclone/cmd/mountlib"
)

const supportsInstant = false

func platform(*mountlib.Options, Request) (mountlib.MountFn, string, error) {
	return nil, "", errors.New("mounting is only supported on Windows and macOS")
}

func changes() int64 { return 0 }
