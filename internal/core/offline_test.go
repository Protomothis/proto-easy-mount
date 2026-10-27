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
	s.mu.Lock()
	state := s.mounts[k].state
	s.mu.Unlock()
	if state == Suspended {
		t.Fatal("suspended drive was not switched back on")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(toasts) != 1 || toasts[0] != "드라이브를 다시 연결했어요" {
		t.Fatalf("notifications = %q", toasts)
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
