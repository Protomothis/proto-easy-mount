// Package tustest provides an in-memory tus server for tests.
package tustest

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeServer is an in-memory tus server for tests: POST to any path
// creates an upload, PATCH appends at the declared offset.
type FakeServer struct {
	mu      sync.Mutex
	uploads map[string]*FakeUpload
	next    int
	// FailPatch, when set, decides whether a PATCH at offset fails with 500.
	FailPatch func(offset int64) bool
	// OnPatch runs before a PATCH body is read (to observe or slow it).
	OnPatch func(offset int64)
	// SlowRead, when set, pauses after every 64 KB of PATCH body read,
	// to stand in for a server slower than the client.
	SlowRead time.Duration
}

// FakeUpload is one upload's state.
type FakeUpload struct {
	Name string
	Dir  string
	Size int64
	Meta map[string]string
	Data []byte
}

// Done reports whether every byte arrived.
func (u *FakeUpload) Done() bool { return int64(len(u.Data)) == u.Size }

// Uploads returns the finished uploads by directory/name.
func (s *FakeServer) Uploads() map[string]*FakeUpload {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*FakeUpload{}
	for _, u := range s.uploads {
		out[strings.TrimSuffix(u.Dir, "/")+"/"+u.Name] = u
	}
	return out
}

func (s *FakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Tus-Resumable") != "1.0.0" {
		http.Error(w, "missing Tus-Resumable", http.StatusPreconditionFailed)
		return
	}
	s.mu.Lock()
	if s.uploads == nil {
		s.uploads = map[string]*FakeUpload{}
	}
	s.mu.Unlock()

	switch r.Method {
	case http.MethodPost:
		size, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
		if err != nil {
			http.Error(w, "bad length", http.StatusBadRequest)
			return
		}
		meta := map[string]string{}
		for _, kv := range strings.Split(r.Header.Get("Upload-Metadata"), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(kv), " ")
			b, _ := base64.StdEncoding.DecodeString(v)
			meta[k] = string(b)
		}
		s.mu.Lock()
		s.next++
		id := fmt.Sprintf("/data/%d", s.next)
		s.uploads[id] = &FakeUpload{Name: meta["filename"], Dir: r.URL.Path, Size: size, Meta: meta}
		s.mu.Unlock()
		w.Header().Set("Location", id)
		w.WriteHeader(http.StatusCreated)
	case http.MethodPatch, http.MethodHead:
		s.mu.Lock()
		u := s.uploads[r.URL.Path]
		s.mu.Unlock()
		if u == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead {
			s.mu.Lock()
			w.Header().Set("Upload-Offset", strconv.Itoa(len(u.Data)))
			s.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		off, _ := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if s.OnPatch != nil {
			s.OnPatch(off)
		}
		if s.FailPatch != nil && s.FailPatch(off) {
			http.Error(w, "injected failure", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		have := int64(len(u.Data))
		s.mu.Unlock()
		if off != have {
			http.Error(w, "offset mismatch", http.StatusConflict)
			return
		}
		var body io.Reader = r.Body
		if s.SlowRead > 0 {
			body = &slowReader{r: r.Body, pause: s.SlowRead}
		}
		b, err := io.ReadAll(body)
		if err != nil {
			return
		}
		s.mu.Lock()
		u.Data = append(u.Data, b...)
		n := len(u.Data)
		s.mu.Unlock()
		w.Header().Set("Upload-Offset", strconv.Itoa(n))
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

type slowReader struct {
	r     io.Reader
	pause time.Duration
}

func (s *slowReader) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10]
	}
	n, err := s.r.Read(p)
	time.Sleep(s.pause)
	return n, err
}
