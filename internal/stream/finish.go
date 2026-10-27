package stream

import (
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
