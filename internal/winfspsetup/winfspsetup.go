// Package winfspsetup finds out whether the WinFsp driver that Windows
// mounts need is installed, and installs it: the official installer of a
// pinned release, checked against its SHA-256, run with an administrator
// prompt. The app's installer does the same (installer/proto-easy-mount.iss);
// scripts/build-windows.ps1 pins the same release.
package winfspsetup

// The pinned WinFsp release.
const (
	Version = "2.1.25156"
	URL     = "https://github.com/winfsp/winfsp/releases/download/v2.1/winfsp-2.1.25156.msi"
	SHA256  = "073a70e00f77423e34bed98b86e600def93393ba5822204fac57a29324db9f7a"
	Page    = "https://winfsp.dev/rel/"
)

// Phase is how far Install got, for showing progress.
type Phase int

const (
	Downloading Phase = iota
	Installing        // the installer runs; Windows asks for administrator rights
)
