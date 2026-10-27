package notify

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The toast is shown by a fixed PowerShell script that reads the toast XML
// from the environment, so no text ever becomes part of a script or a
// command line. (Adapted from Protomothis/smartthings-pc-control, MIT.)
const (
	xmlEnv   = "PEM_TOAST_XML"
	appIDEnv = "PEM_TOAST_APPID"
)

const script = `$ErrorActionPreference = 'Stop'
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml($env:` + xmlEnv + `)
$toast = New-Object Windows.UI.Notifications.ToastNotification $xml
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($env:` + appIDEnv + `).Show($toast)
`

func toastXML(title, text string) string {
	esc := func(s string) string {
		var b strings.Builder
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return `<toast><visual><binding template="ToastGeneric"><text>` + esc(title) + `</text><text>` + esc(text) +
		`</text></binding></visual><audio src="ms-winsoundevent:Notification.Default"/></toast>`
}

// encoded is s in the UTF-16LE base64 form -EncodedCommand takes.
func encoded(s string) string {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func command(title, text string) *exec.Cmd {
	// By absolute path, so a powershell.exe earlier on PATH is never the one run.
	ps := filepath.Join(os.Getenv("SystemRoot"), `System32\WindowsPowerShell\v1.0\powershell.exe`)
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-EncodedCommand", encoded(script))
	cmd.Env = append(os.Environ(), xmlEnv+"="+toastXML(title, text), appIDEnv+"="+AppID)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd
}

func show(title, text string) {
	go func() {
		if out, err := command(title, text).CombinedOutput(); err != nil {
			line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
			slog.Warn(fmt.Sprintf("알림을 띄우지 못함: %v %s", err, line))
		}
	}()
}

var setAppID = windows.NewLazySystemDLL("shell32.dll").NewProc("SetCurrentProcessExplicitAppUserModelID")

// UseAppID gives this process the shortcut's AppUserModelID, so its
// windows group with the Start menu shortcut and its icon on the taskbar.
// Call before the first window opens.
func UseAppID() {
	if setAppID.Find() != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(AppID)
	if err == nil {
		setAppID.Call(uintptr(unsafe.Pointer(p)))
	}
}
