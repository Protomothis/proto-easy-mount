package main

import (
	"fmt"
	"os"

	"fyne.io/systray"
	"golang.org/x/sys/windows"
)

// run drives the tray from the main thread (systray locks it in init).
// Gio windows on Windows run their own message loops on other threads,
// so app.Main is not needed.
func run(onReady, onExit func()) {
	systray.Run(onReady, onExit)
}

// fatal reports startup errors in a message box; there is no console.
func fatal(err error) {
	title, _ := windows.UTF16PtrFromString("Proto Easy Mount")
	msg, _ := windows.UTF16PtrFromString(fmt.Sprint(err))
	windows.MessageBox(0, msg, title, windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
