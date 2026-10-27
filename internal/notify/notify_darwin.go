package notify

import (
	"fmt"
	"log/slog"
	"os/exec"
)

// The text goes in as script arguments, never as part of the script.
const script = `on run argv
display notification (item 2 of argv) with title (item 1 of argv)
end run`

func show(title, text string) {
	go func() {
		if err := exec.Command("osascript", "-e", script, title, text).Run(); err != nil {
			slog.Warn(fmt.Sprintf("알림을 띄우지 못함: %v", err))
		}
	}()
}

// UseAppID does nothing on macOS: the bundle identifies the app.
func UseAppID() {}
