package winfspsetup

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"unsafe"
)

// The app, its installer and the build script install the same release.
func TestPinnedReleaseMatchesBuildScript(t *testing.T) {
	b, err := os.ReadFile("../../scripts/build-windows.ps1")
	if err != nil {
		t.Fatal(err)
	}
	field := func(name string) string {
		m := regexp.MustCompile(`\$` + name + ` = '([^']+)'`).FindSubmatch(b)
		if m == nil {
			t.Fatalf("no $%s in build-windows.ps1", name)
		}
		return string(m[1])
	}
	if v := field("winfspVersion"); v != Version {
		t.Errorf("build script pins %s, app %s", v, Version)
	}
	if u := field("winfspUrl"); u != URL {
		t.Errorf("build script URL %s, app %s", u, URL)
	}
	if s := strings.ToLower(field("winfspSha256")); s != SHA256 {
		t.Errorf("build script SHA-256 %s, app %s", s, SHA256)
	}
}

// SHELLEXECUTEINFOW is 112 bytes on 64-bit Windows.
func TestShellExecuteInfoLayout(t *testing.T) {
	if n := unsafe.Sizeof(shellExecuteInfo{}); n != 112 {
		t.Fatalf("size %d, want 112", n)
	}
}

// Says what this machine has; WinFsp may or may not be installed here.
func TestInstalledReport(t *testing.T) { t.Logf("WinFsp installed: %v", Installed()) }
