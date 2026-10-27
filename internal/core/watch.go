package core

import (
	"context"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/mount"
)

const (
	sampleEvery  = time.Second
	refreshEvery = time.Minute      // catches changes made elsewhere (web, other devices)
	retryEvery   = 10 * time.Second // accounts that failed to load
)

// After uploads finish or files are deleted the quota is read again: soon,
// once a burst of deletes is over, and once more when the server's totals
// have caught up.
var quotaDelays = []time.Duration{2 * time.Second, 15 * time.Second}

// Activity returns the latest transfer speed and pending uploads.
func (s *Service) Activity() Activity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activity
}

// Watch samples live mounts until ctx ends: upload queues and transfer
// speed every second, and drive lists (for quota) every minute and shortly
// after anything changed the space used.
func (s *Service) Watch(ctx context.Context) {
	tick := time.NewTicker(sampleEvery)
	defer tick.Stop()

	lastRefresh, lastRetry := time.Now(), time.Now()
	var lastProbe time.Time
	lastChanges := mount.Changes()
	var due []time.Time // quota re-reads scheduled after a change
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(lastProbe) >= probeEvery {
				lastProbe = now
				s.watchNetwork(ctx)
			}
			changed := s.sample(mount.Transfers())
			if n := mount.Changes(); n != lastChanges {
				lastChanges, changed = n, true
			}
			if changed {
				due = due[:0]
				for _, d := range quotaDelays {
					due = append(due, now.Add(d))
				}
			}
			refresh := now.Sub(lastRefresh) >= refreshEvery
			for len(due) > 0 && !now.Before(due[0]) {
				due, refresh = due[1:], true
			}
			if refresh {
				lastRefresh = now
				for _, id := range s.accountIDs() {
					go s.refreshQuiet(ctx, id)
				}
			} else if now.Sub(lastRetry) >= retryEvery {
				// e.g. started at sign-in before the network was up
				lastRetry = now
				for _, id := range s.failing() {
					go s.refreshQuiet(ctx, id)
				}
			}
		}
	}
}

// sample updates upload counts and speed, notifying on change, and reports
// whether some mount's uploads just finished, so the quota can be re-read.
func (s *Service) sample(transfers []mount.Transfer) (drained bool) {
	type probe struct {
		ms *mountState
		m  *mount.Mounted
	}
	s.mu.Lock()
	var probes []probe
	for _, ms := range s.mounts {
		if ms.m != nil {
			probes = append(probes, probe{ms, ms.m})
		}
	}
	s.mu.Unlock()

	counts := make([]int, len(probes))
	for i, p := range probes {
		inProgress, queued := p.m.Uploads()
		counts[i] = inProgress + queued
	}

	s.mu.Lock()
	changed := false
	total := 0
	for i, p := range probes {
		n := counts[i]
		total += n
		if p.ms.uploads > 0 && n == 0 {
			drained = true
		}
		if p.ms.uploads != n {
			p.ms.uploads = n
			changed = true
		}
	}
	total += mount.InstantUploads()
	up, down := mount.Totals(transfers)
	a := Activity{Up: up, Down: down, Uploads: total}
	for _, t := range transfers {
		if t.Up {
			a.Files = append(a.Files, t)
		}
	}
	if !a.equal(s.activity) {
		s.activity = a
		changed = true
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return drained
}

func (s *Service) accountIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.order...)
}

// refreshQuiet reloads drive lists without showing a loading state;
// failures keep the previous list.
func (s *Service) refreshQuiet(ctx context.Context, id string) {
	s.mu.Lock()
	as := s.accounts[id]
	s.mu.Unlock()
	if as == nil {
		return
	}
	drives, instant, err := s.fetchDrives(ctx, as)
	if err != nil {
		return
	}
	s.loaded(as, drives, instant)
	s.notify()
}

// failing lists accounts whose last load failed for a reason that may pass
// (no network yet, server restarting), so they are retried soon.
func (s *Service) failing() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, id := range s.order {
		if as := s.accounts[id]; as.err != "" && !as.signIn && !as.loading {
			ids = append(ids, id)
		}
	}
	return ids
}
