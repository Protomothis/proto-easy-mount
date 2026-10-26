package mount

import "strings"

// UsesDriveLetters reports whether mount targets are drive letters (Windows)
// rather than directories.
func UsesDriveLetters() bool { return usesDriveLetters }

// FreeDriveLetters lists letters ("D:" … "Z:") not taken by any volume.
// It is empty where drive letters don't exist.
func FreeDriveLetters() []string { return freeLetters(logicalDrives()) }

// DriveLetterFree reports whether a letter like "Z:" is available.
func DriveLetterFree(letter string) bool {
	letter = strings.ToUpper(letter)
	for _, l := range FreeDriveLetters() {
		if l == letter {
			return true
		}
	}
	return false
}

// freeLetters turns a GetLogicalDrives bitmask (bit 0 = A:) into free
// letters, skipping A–C which are reserved for floppies and the system.
func freeLetters(used uint32) []string {
	if !usesDriveLetters {
		return nil
	}
	var out []string
	for i := 3; i < 26; i++ {
		if used&(1<<i) == 0 {
			out = append(out, string(rune('A'+i))+":")
		}
	}
	return out
}
