//go:build !windows

package winfspsetup

import (
	"context"
	"errors"
)

// Needed reports whether this OS needs WinFsp for mounts.
const Needed = false

// Installed is true: only Windows mounts need WinFsp.
func Installed() bool { return true }

// Install is not available here.
func Install(context.Context, func(Phase)) error { return errors.New("winfsp: Windows only") }
