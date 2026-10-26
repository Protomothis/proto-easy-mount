package gui

import (
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/event"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// systemDark reports whether Windows is set to dark mode for apps.
func systemDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

var setWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// darkTitleBar makes the native title bar match the window's theme.
func darkTitleBar(hwnd uintptr, dark bool) {
	if hwnd == 0 || setWindowAttribute.Find() != nil {
		return
	}
	const useImmersiveDarkMode = 20 // DWMWA_USE_IMMERSIVE_DARK_MODE, Windows 10 20H1+
	var on int32
	if dark {
		on = 1
	}
	setWindowAttribute.Call(hwnd, useImmersiveDarkMode, uintptr(unsafe.Pointer(&on)), unsafe.Sizeof(on))
}

// windowHandle extracts the HWND from Gio's view event.
func windowHandle(e event.Event) (uintptr, bool) {
	v, ok := e.(app.Win32ViewEvent)
	return v.HWND, ok
}
