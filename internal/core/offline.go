package core

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/mount"
)

// The network watch: while drives are connected, each account's server is
// probed. When it stops answering, its drives are switched off (Explorer
// would otherwise hang on them) and come back on by themselves once it
// answers again. Uploads waiting in the write cache are kept on disk and
// continue after the drive is back.
const (
	probeEvery   = 10 * time.Second
	probeTimeout = 5 * time.Second
	offlineAfter = 3 // failed probes in a row (about 30s) before switching off
	onlineAfter  = 2 // answered probes in a row before switching back on
)

var probeClient = &http.Client{
	Timeout: probeTimeout,
	// any answer counts; following a redirect to a login page proves nothing more
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// health is the network watch's view of one account's server.
type health struct {
	probing  bool
	fails    int
	answered int
	offline  bool
}

// AutoSuspend reports whether drives are switched off while their server
// is unreachable.
func (s *Service) AutoSuspend() bool {
	s.prefs.mu.Lock()
	defer s.prefs.mu.Unlock()
	return !s.prefs.KeepMountedOffline
}

// SetAutoSuspend chooses whether drives are switched off while their server
// is unreachable.
func (s *Service) SetAutoSuspend(on bool) error {
	s.prefs.mu.Lock()
	defer s.prefs.mu.Unlock()
	s.prefs.KeepMountedOffline = !on
	return s.prefs.save()
}

// watchNetwork probes the servers of accounts with drives connected or
// switched off by the watch. Called from Watch every probeEvery.
func (s *Service) watchNetwork(ctx context.Context) {
	s.mu.Lock()
	var ids []string
	for k, ms := range s.mounts {
		as := s.accounts[k.acct]
		if as == nil || as.health.probing || (ms.state != Mounted && ms.state != Suspended) {
			continue
		}
		as.health.probing = true
		ids = append(ids, k.acct)
	}
	s.mu.Unlock()
	for _, id := range ids {
		go s.probe(ctx, id)
	}
}

// probe asks the account's server for anything and acts on the result.
func (s *Service) probe(ctx context.Context, id string) {
	s.mu.Lock()
	as := s.accounts[id]
	s.mu.Unlock()
	if as == nil {
		return
	}
	ok := reachable(ctx, as.acct.Server)

	s.mu.Lock()
	h := &as.health
	h.probing = false
	if ok {
		h.fails, h.answered = 0, h.answered+1
	} else {
		h.fails, h.answered = h.fails+1, 0
	}
	goOffline := !ok && !h.offline && h.fails >= offlineAfter
	goOnline := ok && h.offline && h.answered >= onlineAfter
	if goOffline {
		h.offline = true
	}
	if goOnline {
		h.offline = false
	}
	offline := h.offline
	s.mu.Unlock()

	switch {
	case goOffline:
		slog.Warn(fmt.Sprintf("서버 응답 없음 %s (%d번 연속)", id, offlineAfter))
	case goOnline:
		slog.Info(fmt.Sprintf("서버 응답 돌아옴 %s", id))
		s.resume(id)
	}
	if offline && s.AutoSuspend() {
		s.suspend(id) // again on later probes: drives held by uploads go when those end
	}
}

// reachable reports whether the server answers at all. A 5xx from a proxy
// in front of a stopped server counts as no answer.
func reachable(ctx context.Context, server string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(server, "/")+"/status.php", nil)
	if err != nil {
		return false
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

// suspend switches off the account's connected drives, keeping them
// remembered. While an instant upload is still being sent (it may yet
// reach the server, or moves to the write cache when it gives up), drives
// stay on; the next probe tries again.
func (s *Service) suspend(id string) {
	if mount.InstantUploads() > 0 {
		return
	}
	s.mu.Lock()
	var stop []*mount.Mounted
	var names []string
	for k, ms := range s.mounts {
		if k.acct != id || ms.state != Mounted || ms.m == nil {
			continue
		}
		ms.suspended, ms.stopping = true, true
		stop = append(stop, ms.m)
		names = append(names, ms.target)
	}
	s.mu.Unlock()
	if len(stop) == 0 {
		return
	}
	slog.Warn(fmt.Sprintf("서버에 연결할 수 없어 드라이브를 잠시 끔 %s: %s", id, strings.Join(names, ", ")))
	s.toast("드라이브를 잠시 껐어요",
		fmt.Sprintf("서버에 연결할 수 없어 %s 드라이브를 껐어요. 연결되면 다시 켜요.", strings.Join(names, ", ")))
	for _, m := range stop {
		go m.Unmount() // may wait for requests stuck on the network
	}
	s.notify()
}

// resume switches the drives the network watch turned off back on.
func (s *Service) resume(id string) {
	s.mu.Lock()
	var drives, names []string
	for k, ms := range s.mounts {
		if k.acct == id && ms.state == Suspended {
			drives = append(drives, k.drive)
			names = append(names, ms.target)
		}
	}
	s.mu.Unlock()
	if len(drives) == 0 {
		return
	}
	slog.Info(fmt.Sprintf("드라이브 다시 연결 %s: %s", id, strings.Join(names, ", ")))
	for _, d := range drives {
		s.Mount(id, d)
	}
	s.toast("드라이브를 다시 연결했어요", fmt.Sprintf("서버에 다시 연결돼 %s 드라이브를 켰어요.", strings.Join(names, ", ")))
}

func (s *Service) toast(title, text string) {
	if s.Notify != nil {
		s.Notify(title, text)
	}
}
