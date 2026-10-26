//go:build !windows

package mount

const usesDriveLetters = false

func logicalDrives() uint32 { return 0 }
