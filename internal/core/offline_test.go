package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/protomothis/proto-easy-mount/internal/provider"
	"github.com/protomothis/proto-easy-mount/internal/store"
)

// A server that stops answering is marked offline after a few probes, and
// drives switched off for it come back (with a notification) once it
// answers again.
func TestNetworkWatch(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway) // a proxy in front of a stopped server
			return
		}
		w.Write([]byte(`{"installed":true}`))
	}))
	defer srv.Close()

	keyring.MockInit()
	*theFlaky = flaky{}
	d := t.TempDir()
	st := &store.Store{ConfigDir: d + "/cfg", CacheDir: d + "/cache"}
	acct, err := st.Save(provider.Account{Provider: "flaky", Server: srv.URL, User: "u"},
		provider.Secret{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, Env(func(string) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var toasts []string
	s.Notify = func(title, _ string) { mu.Lock(); toasts = append(toasts, title); mu.Unlock() }
	s.Refresh(context.Background(), acct.ID)

	ctx := context.Background()
	offline := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.accounts[acct.ID].health.offline
	}

	down.Store(true)
	for i := 0; i < offlineAfter-1; i++ {
		s.probe(ctx, acct.ID)
	}
	if offline() {
		t.Fatal("offline before enough failed probes")
	}
	s.probe(ctx, acct.ID)
	if !offline() {
		t.Fatal("still online after failed probes")
	}

	// a drive the watch switched off
	k := mountKey{acct.ID, "me"}
	s.mu.Lock()
	s.mounts[k] = &mountState{state: Suspended, target: "X:"}
	s.mu.Unlock()

	down.Store(false)
	s.probe(ctx, acct.ID)
	if !offline() {
		t.Fatal("online after a single answer")
	}
	s.probe(ctx, acct.ID)
	if offline() {
		t.Fatal("still offline after the server answered")
	}
	// the test provider can't really mount: the attempt fails and the
	// drive goes back to waiting, with a later retry and no "back" notice
	ms := settled(t, s, k)
	if ms.state != Suspended || ms.tries != 1 || !ms.nextTry.After(time.Now()) {
		t.Fatalf("after a failed resume: state %v, tries %d, next %v", ms.state, ms.tries, ms.nextTry)
	}
	s.probe(ctx, acct.ID) // too soon: no new attempt
	if ms := settled(t, s, k); ms.tries != 1 {
		t.Fatalf("retried before the wait was over (%d tries)", ms.tries)
	}
	s.mu.Lock()
	s.mounts[k].nextTry = time.Now().Add(-time.Second)
	s.mu.Unlock()
	s.probe(ctx, acct.ID)
	if ms := settled(t, s, k); ms.tries != 2 || ms.state != Suspended {
		t.Fatalf("second attempt: state %v, tries %d", ms.state, ms.tries)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(toasts) != 0 {
		t.Fatalf("notifications = %q", toasts)
	}
	if w := resumeBackoff(1); w != probeEvery {
		t.Fatalf("first wait %v", w)
	}
	if w := resumeBackoff(20); w != 5*time.Minute {
		t.Fatalf("longest wait %v", w)
	}
}

// settled waits for a mount attempt to end and returns its state.
func settled(t *testing.T, s *Service, k mountKey) mountState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		ms := *s.mounts[k]
		s.mu.Unlock()
		if ms.state != Connecting || time.Now().After(deadline) {
			return ms
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Switching a suspended drive off by hand keeps it off.
func TestUnmountSuspended(t *testing.T) {
	s, id := flakyService(t, false)
	s.RefreshAll(context.Background())
	k := mountKey{id, "me"}
	s.mu.Lock()
	s.mounts[k] = &mountState{state: Suspended, target: "X:", suspended: true}
	s.mu.Unlock()
	s.Unmount(id, "me")
	s.mu.Lock()
	defer s.mu.Unlock()
	if ms := s.mounts[k]; ms.state != Idle || ms.suspended {
		t.Fatalf("after switching off: %+v", ms)
	}
}
