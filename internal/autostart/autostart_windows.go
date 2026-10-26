package autostart

import (
	"errors"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Enabled reports whether the app starts at login from its current location.
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(Name)
	if err != nil {
		return false
	}
	exe, err := command()
	return err == nil && strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// Set turns starting at login on or off.
func Set(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(Name); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return err
		}
		return nil
	}
	exe, err := command()
	if err != nil {
		return err
	}
	return k.SetStringValue(Name, `"`+exe+`" `+BackgroundFlag)
}
