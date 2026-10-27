// Package notify shows system notifications: Windows toasts, macOS
// notification banners.
package notify

// AppID is the AppUserModelID toasts are shown under. Windows shows a
// toast's banner only for an ID carried by a Start menu shortcut, which
// the installer creates (installer/proto-easy-mount.iss); builds run
// without it still land in the notification center.
const AppID = "Protomothis.ProtoEasyMount"

// Show shows a notification and returns without waiting for it; failures
// only go to the log.
func Show(title, text string) { show(title, text) }
