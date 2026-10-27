package stream

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Once the server has all of a file it still has to finish the upload
// before answering: OpenCloud reads the whole file back to compute its
// checksums, about 10 s per GB on a hard disk. finishRate learns that speed
// from finished uploads, so the app can say how long the wait will be.
var finishRate = struct {
	mu      sync.Mutex
	rate    float64 // bytes per second
	learned bool    // rate comes from a real upload, not the default
}{rate: defaultFinishRate}

const (
	defaultFinishRate = 100 << 20 // measured: OpenCloud on a hard disk
	defaultLearnFrom  = 64 << 20  // smaller files finish too fast to measure
)

var learnFrom int64 = defaultLearnFrom

// learnFinish takes the time a size-byte upload took from its last byte
// sent to the server's answer.
func learnFinish(size int64, took time.Duration) {
	if size < learnFrom || took <= 0 {
		return
	}
	r := float64(size) / took.Seconds()
	finishRate.mu.Lock()
	if finishRate.learned {
		finishRate.rate = 0.5*finishRate.rate + 0.5*r // follow changes, smooth out outliers
	} else {
		finishRate.rate, finishRate.learned = r, true // any measurement beats the default
	}
	finishRate.mu.Unlock()
}

// finishLeft estimates how much longer the server needs for a size-byte
// upload whose last byte went out at sentAt; 0 when the estimate ran out.
func finishLeft(size int64, sentAt time.Time) time.Duration {
	finishRate.mu.Lock()
	rate := finishRate.rate
	finishRate.mu.Unlock()
	total := time.Duration(float64(size) / rate * float64(time.Second))
	return max(total-time.Since(sentAt), 0)
}

// allSent reports whether every byte has gone out to the server.
func (u *Upload) allSent() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.readPos >= u.size
}

// fileURL is the WebDAV URL of the file being uploaded.
func (u *Upload) fileURL() string {
	return strings.TrimRight(u.coll, "/") + "/" + url.PathEscape(u.name)
}

// landed reports whether an upload whose last PATCH failed reached the
// server anyway. OpenCloud answers that PATCH only after reading the whole
// file back, and its tus layer gives up on the reply after 120 s although
// the upload goes on to succeed (opencloud-eu/opencloud#3679): about 12 GB
// on a hard disk. Resending would cost the whole file again, so wait for
// the file to show up with its size (and, when it replaces one, a new
// ETag) for as long as finishing it should take, and then some.
func (u *Upload) landed(ctx context.Context, target string) bool {
	c := u.cfg.Client
	if off, err := c.Offset(ctx, target); err == nil && off < u.size {
		return false // bytes are missing: resending them is right
	}
	// a finished upload's session may be gone (HEAD fails); the file tells
	finishRate.mu.Lock()
	rate := finishRate.rate
	finishRate.mu.Unlock()
	deadline := time.Now().Add(2*time.Duration(float64(u.size)/rate*float64(time.Second)) + landedGrace)
	for {
		fi, ok, err := c.Stat(ctx, u.fileURL())
		if err == nil && ok && fi.Size == u.size && (u.before == "" || fi.ETag != u.before) {
			return true
		}
		if u.isAborted() || time.Now().After(deadline) {
			return false
		}
		time.Sleep(landedPoll)
	}
}

// How often landed asks for the file, and how long past the estimate.
var (
	landedPoll  = 3 * time.Second
	landedGrace = 2 * time.Minute
)
