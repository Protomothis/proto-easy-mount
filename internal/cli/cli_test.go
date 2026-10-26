package cli

import (
	"testing"

	"github.com/protomothis/proto-easy-mount/internal/provider"
)

func TestPickDrives(t *testing.T) {
	drives := []provider.Drive{
		{ID: "me", Name: "Alice", Kind: provider.KindPersonal},
		{ID: "p1", Name: "Family", Kind: provider.KindProject},
	}
	if got, _ := pickDrives(drives, nil, false); len(got) != 1 || got[0].ID != "me" {
		t.Errorf("default = %+v", got)
	}
	if got, _ := pickDrives(drives, []string{"family"}, false); len(got) != 1 || got[0].ID != "p1" {
		t.Errorf("by name = %+v", got)
	}
	if got, _ := pickDrives(drives, nil, true); len(got) != 2 {
		t.Errorf("all = %+v", got)
	}
	if _, err := pickDrives(drives, []string{"nope"}, false); err == nil {
		t.Error("unknown drive accepted")
	}
	if _, err := pickDrives(drives[1:], nil, false); err == nil {
		t.Error("missing personal drive not reported")
	}
}
