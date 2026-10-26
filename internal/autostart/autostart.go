// Package autostart registers the desktop app to start at user login.
package autostart

import "os"

// Name identifies the entry in the OS startup list.
const Name = "ProtoEasyMount"

// BackgroundFlag tells the app it was started at login, so it should stay
// in the tray instead of opening its window.
const BackgroundFlag = "--background"

func command() (string, error) {
	return os.Executable()
}
