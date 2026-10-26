package gui

import (
	"os/exec"

	"gioui.org/io/event"
)

// systemDark reports whether macOS is in dark mode.
func systemDark() bool {
	out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
	return err == nil && len(out) >= 4 && string(out[:4]) == "Dark"
}

// The title bar follows the system appearance on its own.
func darkTitleBar(uintptr, bool) {}

func windowHandle(event.Event) (uintptr, bool) { return 0, false }
