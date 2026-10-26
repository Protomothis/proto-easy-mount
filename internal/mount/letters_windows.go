package mount

import "golang.org/x/sys/windows"

const usesDriveLetters = true

func logicalDrives() uint32 {
	m, _ := windows.GetLogicalDrives()
	return m
}
