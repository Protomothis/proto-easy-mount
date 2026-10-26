package core

import (
	"slices"
	"testing"

	"github.com/protomothis/proto-easy-mount/internal/mount"
	"github.com/protomothis/proto-easy-mount/internal/store"
)

func newService(t *testing.T) *Service {
	d := t.TempDir()
	s, err := New(&store.Store{ConfigDir: d + "/cfg", CacheDir: d + "/cache"}, Env(nil))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPrefsTargets(t *testing.T) {
	dir := t.TempDir()
	p, _ := loadPrefs(dir)
	p.setTarget("a", "me", "X:")
	p.setMounted("a", "me", true)
	if a, d := p.targetOwner("x:"); a != "a" || d != "me" {
		t.Fatalf("owner = %s/%s", a, d)
	}
	// survives a reload
	p2, err := loadPrefs(dir)
	if err != nil || p2.target("a", "me") != "X:" || len(p2.remembered("a")) != 1 {
		t.Fatalf("reloaded = %+v, %v", p2, err)
	}
	p2.setTarget("a", "me", "")
	if p2.target("a", "me") != "" || p2.Targets != nil && len(p2.Targets["a"]) != 0 {
		t.Fatalf("auto not restored: %+v", p2.Targets)
	}
	p2.setTarget("a", "me", "Y:")
	p2.forget("a")
	if p2.target("a", "me") != "" || len(p2.remembered("a")) != 0 {
		t.Fatal("forget kept state")
	}
}

func TestAvailableTargetsHonoursClaims(t *testing.T) {
	free := mount.FreeDriveLetters()
	if len(free) == 0 {
		t.Skip("no drive letters on this OS")
	}
	s := newService(t)
	letter := free[0]
	if err := s.SetTarget("acct", "one", letter); err != nil {
		t.Fatal(err)
	}
	if got := s.AvailableTargets("acct", "two"); slices.Contains(got, letter) {
		t.Errorf("%s claimed by another drive but offered: %v", letter, got)
	}
	if got := s.AvailableTargets("acct", "one"); !slices.Contains(got, letter) {
		t.Errorf("owner can't see its own letter %s: %v", letter, got)
	}
}

func TestPickTargetClaimsAutomaticLetter(t *testing.T) {
	if len(mount.FreeDriveLetters()) < 2 {
		t.Skip("needs two free drive letters")
	}
	s := newService(t)
	a, err := s.pickTarget("acct", "one")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.pickTarget("acct", "two")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two drives got the same letter %s", a)
	}
	if s.prefs.target("acct", "one") != a {
		t.Fatal("automatic pick not remembered")
	}
}

func TestPrefsModes(t *testing.T) {
	dir := t.TempDir()
	p, _ := loadPrefs(dir)
	if p.mode("a", "me") != Stable {
		t.Fatal("default mode not stable")
	}
	p.setMode("a", "me", Instant)
	p2, _ := loadPrefs(dir)
	if p2.mode("a", "me") != Instant {
		t.Fatal("instant mode not persisted")
	}
	p2.setMode("a", "me", Stable)
	if p2.Modes != nil && len(p2.Modes["a"]) != 0 {
		t.Fatalf("stable should clear the entry: %+v", p2.Modes)
	}
	p2.setMode("a", "me", Instant)
	p2.forget("a")
	if p2.mode("a", "me") != Stable {
		t.Fatal("forget kept the mode")
	}
}
