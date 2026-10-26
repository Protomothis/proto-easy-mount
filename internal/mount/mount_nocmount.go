//go:build windows && !cmount

package mount

import (
	"errors"

	"github.com/rclone/rclone/cmd/mountlib"
)

const supportsInstant = false

func platform(*mountlib.Options, Request) (mountlib.MountFn, string, error) {
	return nil, "", errors.New("this build has no mount support; rebuild with -tags cmount")
}

func changes() int64 { return 0 }
