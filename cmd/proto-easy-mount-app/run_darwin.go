package main

import (
	"fmt"
	"os"

	"fyne.io/systray"
	"gioui.org/app"
)

// run hands the main thread to Gio's Cocoa loop and attaches the menu bar
// item to it. Not yet built or tried on a Mac (needs cgo).
func run(onReady, onExit func()) {
	start, _ := systray.RunWithExternalLoop(onReady, onExit)
	start()
	app.Main()
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "proto-easy-mount:", err)
	os.Exit(1)
}
