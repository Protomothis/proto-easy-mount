package update

import (
	"context"
	"os"
	"testing"
)

// Checks the published releases the way an installed app does:
//
//	PEM_LIVE_FROM=0.1.0-rc.1 go test -run TestLiveUpdate ./internal/update/
func TestLiveUpdate(t *testing.T) {
	from := os.Getenv("PEM_LIVE_FROM")
	if from == "" {
		t.Skip("set PEM_LIVE_FROM to the version to update from")
	}
	a, err := Check(context.Background(), from, false)
	if err != nil {
		t.Fatalf("check from %s: %v", from, err)
	}
	if a == nil {
		t.Skipf("%s is the newest release", from)
	}
	path, err := a.Download(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s → %s, notes %d bytes, installer %s", from, a.Version, len(a.Notes), path)
}
