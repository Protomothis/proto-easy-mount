// Package browser opens URLs in the user's default browser.
package browser

import (
	"os/exec"
	"runtime"
)

// OpenPath shows a folder or drive in the file manager.
func OpenPath(path string) error {
	switch runtime.GOOS {
	case "windows":
		if len(path) == 2 && path[1] == ':' {
			path += `\`
		}
		return exec.Command("explorer", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

// Open launches the default browser at url.
func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
