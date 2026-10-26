//go:build !windows && !darwin

package gui

import "gioui.org/io/event"

func systemDark() bool { return false }

func darkTitleBar(uintptr, bool) {}

func windowHandle(event.Event) (uintptr, bool) { return 0, false }
