package core

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Prefs is app state worth keeping between runs.
type Prefs struct {
	// Mounted remembers which drives the user left connected, per account,
	// so they come back on the next start.
	Mounted map[string][]string `json:"mounted,omitempty"`
	// Targets holds the chosen drive letter (or directory) per account and drive.
	Targets map[string]map[string]string `json:"targets,omitempty"`
	// Modes holds drives set to a non-default upload mode.
	Modes map[string]map[string]Mode `json:"modes,omitempty"`
	// TestUpdates also offers test versions (vX.Y.Z-rc.N) as updates.
	TestUpdates bool `json:"test_updates,omitempty"`

	path string
	mu   sync.Mutex
}

func loadPrefs(dir string) (*Prefs, error) {
	p := &Prefs{path: filepath.Join(dir, "prefs.json")}
	b, err := os.ReadFile(p.path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *Prefs) remembered(acct string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.Mounted[acct])
}

func (p *Prefs) setMounted(acct, drive string, on bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Mounted == nil {
		p.Mounted = map[string][]string{}
	}
	list := slices.DeleteFunc(p.Mounted[acct], func(d string) bool { return d == drive })
	if on {
		list = append(list, drive)
	}
	if len(list) == 0 {
		delete(p.Mounted, acct)
	} else {
		p.Mounted[acct] = list
	}
	return p.save()
}

func (p *Prefs) target(acct, drive string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Targets[acct][drive]
}

// targetOwner returns which drive of which account claims target, if any.
func (p *Prefs) targetOwner(target string) (acct, drive string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for a, m := range p.Targets {
		for d, t := range m {
			if strings.EqualFold(t, target) {
				return a, d
			}
		}
	}
	return "", ""
}

func (p *Prefs) setTarget(acct, drive, target string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if target == "" {
		delete(p.Targets[acct], drive)
		if len(p.Targets[acct]) == 0 {
			delete(p.Targets, acct)
		}
		return p.save()
	}
	if p.Targets == nil {
		p.Targets = map[string]map[string]string{}
	}
	if p.Targets[acct] == nil {
		p.Targets[acct] = map[string]string{}
	}
	p.Targets[acct][drive] = target
	return p.save()
}

func (p *Prefs) mode(acct, drive string) Mode {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Modes[acct][drive]
}

func (p *Prefs) setMode(acct, drive string, m Mode) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m == Stable {
		delete(p.Modes[acct], drive)
		if len(p.Modes[acct]) == 0 {
			delete(p.Modes, acct)
		}
		return p.save()
	}
	if p.Modes == nil {
		p.Modes = map[string]map[string]Mode{}
	}
	if p.Modes[acct] == nil {
		p.Modes[acct] = map[string]Mode{}
	}
	p.Modes[acct][drive] = m
	return p.save()
}

func (p *Prefs) forget(acct string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.Mounted, acct)
	delete(p.Targets, acct)
	delete(p.Modes, acct)
	return p.save()
}

func (p *Prefs) save() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.path, b, 0o600)
}

// TestUpdates reports whether test versions are offered as updates.
func (s *Service) TestUpdates() bool {
	s.prefs.mu.Lock()
	defer s.prefs.mu.Unlock()
	return s.prefs.TestUpdates
}

// SetTestUpdates chooses whether test versions are offered as updates.
func (s *Service) SetTestUpdates(on bool) error {
	s.prefs.mu.Lock()
	defer s.prefs.mu.Unlock()
	s.prefs.TestUpdates = on
	return s.prefs.save()
}
