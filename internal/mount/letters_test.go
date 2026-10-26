package mount

import (
	"slices"
	"testing"
)

func TestFreeLetters(t *testing.T) {
	if !usesDriveLetters {
		t.Skip("no drive letters on this OS")
	}
	// A:, C:, D: and Z: in use
	got := freeLetters(1<<0 | 1<<2 | 1<<3 | 1<<25)
	if slices.Contains(got, "C:") || slices.Contains(got, "D:") || slices.Contains(got, "Z:") || slices.Contains(got, "B:") {
		t.Fatalf("used or reserved letters offered: %v", got)
	}
	if got[0] != "E:" || got[len(got)-1] != "Y:" || len(got) != 21 {
		t.Fatalf("free = %v", got)
	}
}
