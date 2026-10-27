//go:build !windows

package update

// InstallerDefault is true: only the Windows installer offers the choice.
func InstallerDefault() bool { return true }
