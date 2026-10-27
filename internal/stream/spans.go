package stream

import (
	"sort"
	"sync"
	"time"
)

// spans is a set of written byte ranges, kept sorted and merged.
type spans []span

type span struct{ from, to int64 } // [from, to)

func (s *spans) add(from, to int64) {
	if to <= from {
		return
	}
	all := append(*s, span{from, to})
	sort.Slice(all, func(i, j int) bool { return all[i].from < all[j].from })
	merged := all[:0]
	for _, x := range all {
		if n := len(merged); n > 0 && x.from <= merged[n-1].to {
			if x.to > merged[n-1].to {
				merged[n-1].to = x.to
			}
			continue
		}
		merged = append(merged, x)
	}
	*s = merged
}

// prefix is the end of the range that starts at 0, i.e. how many leading
// bytes are complete.
func (s spans) prefix() int64 {
	if len(s) == 0 || s[0].from > 0 {
		return 0
	}
	return s[0].to
}

// registry tracks live uploads for progress display.
var (
	regMu  sync.Mutex
	active = map[*Upload]struct{}{}
)

func register(u *Upload) {
	regMu.Lock()
	active[u] = struct{}{}
	regMu.Unlock()
}

func unregister(u *Upload) {
	regMu.Lock()
	delete(active, u)
	regMu.Unlock()
}

// Status describes one live upload.
type Status struct {
	Name       string
	Sent, Size int64
	Speed      int64
	Waiting    bool          // all data sent; the server is finishing (checksums)
	Left       time.Duration // while Waiting: estimated time to the server's answer, 0 if unknown
}

// Active lists live uploads.
func Active() []Status {
	regMu.Lock()
	us := make([]*Upload, 0, len(active))
	for u := range active {
		us = append(us, u)
	}
	regMu.Unlock()
	out := make([]Status, 0, len(us))
	for _, u := range us {
		if u.Committed() { // held-back files are still just local
			out = append(out, u.Status())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
