package winfspsetup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Needed reports whether this OS needs WinFsp for mounts.
const Needed = true

// Installed reports whether WinFsp is there: its install folder in the
// registry and the 64-bit DLL in it, as cgofuse loads it.
func Installed() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\WinFsp`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	dir, _, err := k.GetStringValue("InstallDir")
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "bin", "winfsp-x64.dll"))
	return err == nil
}

// ErrCancelled is returned when the administrator prompt was declined.
var ErrCancelled = errors.New("winfsp: installation cancelled")

// Install downloads the pinned installer, checks it and runs it, telling
// phase what it is doing. It returns when the installer has finished.
func Install(ctx context.Context, phase func(Phase)) error {
	phase(Downloading)
	msi := filepath.Join(os.TempDir(), "proto-easy-mount-winfsp-"+Version+".msi")
	defer os.Remove(msi)
	if err := download(ctx, msi); err != nil {
		return err
	}
	phase(Installing)
	code, err := runElevated("msiexec.exe", `/i "`+msi+`" /passive /norestart`)
	if err != nil {
		return err
	}
	switch code {
	case 0, 3010: // 3010: done, a restart completes it
	case 1602:
		return ErrCancelled
	default:
		return fmt.Errorf("winfsp: installer exited with %d", code)
	}
	if !Installed() {
		return errors.New("winfsp: installer finished but WinFsp is not there")
	}
	return nil
}

func download(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("winfsp: download: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != SHA256 {
		return fmt.Errorf("winfsp: downloaded installer has SHA-256 %s, want %s", sum, SHA256)
	}
	return nil
}

// shellExecuteInfo is SHELLEXECUTEINFOW.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// runElevated starts file with an administrator prompt ("runas") and waits
// for it, returning its exit code.
func runElevated(file, params string) (uint32, error) {
	const seeMaskNoCloseProcess = 0x40
	verb, _ := windows.UTF16PtrFromString("runas")
	f, _ := windows.UTF16PtrFromString(file)
	p, _ := windows.UTF16PtrFromString(params)
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess, lpVerb: verb, lpFile: f, lpParameters: p, nShow: windows.SW_SHOWNORMAL}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, err := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return 0, ErrCancelled
		}
		return 0, fmt.Errorf("winfsp: start installer: %w", err)
	}
	defer windows.CloseHandle(info.hProcess)
	if _, err := windows.WaitForSingleObject(info.hProcess, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return 0, err
	}
	return code, nil
}
