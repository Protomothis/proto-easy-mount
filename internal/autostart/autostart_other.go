//go:build !windows && !darwin

package autostart

import "errors"

// Enabled always reports false on unsupported systems.
func Enabled() bool { return false }

// Set is not supported here.
func Set(bool) error { return errors.New("start at login is not supported on this system") }
