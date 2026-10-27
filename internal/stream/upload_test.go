package stream

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/protomothis/proto-easy-mount/internal/tus"
	"github.com/protomothis/proto-easy-mount/internal/tus/tustest"
)

func setup(t *testing.T, fake *tustest.FakeServer, chunk, window int64) (Config, string) {
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := Config{
		Client:    &tus.Client{HTTP: srv.Client()},
		ShadowDir: t.TempDir(),
		Chunk:     chunk,
		Window:    window,
		Tries:     3,
	}
	return cfg, srv.URL + "/dav/spaces/s/dir/"
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func waitOK(t *testing.T, u *Upload) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := u.Wait(ctx); err != nil {
		t.Fatalf("wait: %v", err)
	}
}

func TestStreamsInOrderAndOutOfOrder(t *testing.T) {
	fake := &tustest.FakeServer{}
	cfg, coll := setup(t, fake, 1000, 1<<20)
	data := randomBytes(5000)

	u, err := Start(context.Background(), cfg, coll, "f.bin", int64(len(data)), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	// Explorer-like: several blocks in flight, arriving out of order
	order := []int{1, 0, 3, 2, 4}
	for _, i := range order {
		off := i * 1000
		if _, err := u.WriteAt(data[off:off+1000], int64(off)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if !u.Complete() {
		t.Fatal("not complete after all writes")
	}
	waitOK(t, u)
	got := fake.Uploads()["/dav/spaces/s/dir/f.bin"]
	if got == nil || !bytes.Equal(got.Data, data) {
		t.Fatal("server data differs")
	}
}

// Writers may not run further ahead of what is on the wire than the
// window, so with a slow server the copy proceeds at the server's pace.
// (How much the wire itself absorbs is up to the OS; loopback takes a lot.)
func TestBackpressure(t *testing.T) {
	fake := &tustest.FakeServer{SlowRead: time.Millisecond}
	const window = 1 << 20
	cfg, coll := setup(t, fake, 0, window) // one continuous PATCH
	const size = 16 << 20
	u, err := Start(context.Background(), cfg, coll, "slow.bin", size, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	block := make([]byte, 256<<10)
	var worst int64
	for off := int64(0); off < size; off += int64(len(block)) {
		if _, err := u.WriteAt(block, off); err != nil {
			t.Fatal(err)
		}
		if ahead := off + int64(len(block)) - u.Status().Sent; ahead > worst {
			worst = ahead
		}
	}
	if worst > window+int64(len(block)) {
		t.Fatalf("writer got %d KB ahead of the wire, window is %d KB", worst>>10, window>>10)
	}
	waitOK(t, u)
}

// By default a whole file goes in one PATCH, so the stream never pauses
// at request boundaries.
func TestContinuousSinglePatch(t *testing.T) {
	var patches atomic.Int32
	fake := &tustest.FakeServer{OnPatch: func(int64) { patches.Add(1) }}
	cfg, coll := setup(t, fake, 0, 1<<20)
	data := randomBytes(8 << 20)
	u, err := Start(context.Background(), cfg, coll, "one.bin", int64(len(data)), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	for off := 0; off < len(data); off += 1 << 20 {
		if _, err := u.WriteAt(data[off:off+1<<20], int64(off)); err != nil {
			t.Fatal(err)
		}
	}
	waitOK(t, u)
	if n := patches.Load(); n != 1 {
		t.Fatalf("%d PATCH requests, want 1", n)
	}
	if got := fake.Uploads()["/dav/spaces/s/dir/one.bin"]; !bytes.Equal(got.Data, data) {
		t.Fatal("server data differs")
	}
}

func TestRewriteOfSentDataIsRefused(t *testing.T) {
	fake := &tustest.FakeServer{}
	cfg, coll := setup(t, fake, 1000, 1<<20)
	data := randomBytes(3000)
	u, _ := Start(context.Background(), cfg, coll, "r.bin", 3000, time.Time{})
	defer u.Close()
	u.WriteAt(data[:2000], 0)
	deadline := time.Now().Add(5 * time.Second)
	for u.Status().Sent < 1000 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := u.WriteAt([]byte("x"), 10); !errors.Is(err, ErrRewrite) {
		t.Fatalf("rewrite accepted: %v", err)
	}
	if _, err := u.WriteAt(make([]byte, 10), 2995); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("write past size accepted: %v", err)
	}
	u.Abort()
}

func TestTransientFailureResumes(t *testing.T) {
	var failed atomic.Bool
	fake := &tustest.FakeServer{FailPatch: func(off int64) bool {
		return off == 1000 && !failed.Swap(true) // fail once, mid-file
	}}
	cfg, coll := setup(t, fake, 1000, 1<<20)
	data := randomBytes(3000)
	u, _ := Start(context.Background(), cfg, coll, "t.bin", 3000, time.Time{})
	defer u.Close()
	u.WriteAt(data, 0)
	waitOK(t, u)
	if got := fake.Uploads()["/dav/spaces/s/dir/t.bin"]; !bytes.Equal(got.Data, data) {
		t.Fatal("server data differs after resume")
	}
}

// When the server keeps failing the upload reports it, and the shadow
// still holds the whole file for the write cache to take over.
func TestPermanentFailureKeepsShadow(t *testing.T) {
	fake := &tustest.FakeServer{FailPatch: func(int64) bool { return true }}
	cfg, coll := setup(t, fake, 1000, 1000)
	data := randomBytes(3000)
	u, _ := Start(context.Background(), cfg, coll, "x.bin", 3000, time.Time{})
	defer u.Close()
	// backpressure must not deadlock once sending has failed
	if _, err := u.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := u.Wait(ctx); err == nil {
		t.Fatal("failure not reported")
	}
	got := make([]byte, 3000)
	if _, err := u.Shadow().ReadAt(got, 0); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("shadow lost data: %v", err)
	}
}

func TestAbort(t *testing.T) {
	fake := &tustest.FakeServer{}
	cfg, coll := setup(t, fake, 1000, 1<<20)
	u, _ := Start(context.Background(), cfg, coll, "a.bin", 3000, time.Time{})
	u.WriteAt(make([]byte, 500), 0)
	u.Abort()
	if err := u.Wait(context.Background()); !errors.Is(err, ErrAborted) {
		t.Fatalf("wait after abort: %v", err)
	}
	if _, err := u.WriteAt(make([]byte, 10), 600); !errors.Is(err, ErrAborted) {
		t.Fatalf("write after abort: %v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if len(Active()) != 0 {
		t.Fatal("aborted upload still listed")
	}
}

func TestSpans(t *testing.T) {
	var s spans
	s.add(10, 20)
	if s.prefix() != 0 {
		t.Fatal("prefix without start")
	}
	s.add(0, 5)
	s.add(5, 10)
	s.add(30, 40)
	if s.prefix() != 20 || len(s) != 2 {
		t.Fatalf("spans = %v", s)
	}
	s.add(15, 35)
	if s.prefix() != 40 || len(s) != 1 {
		t.Fatalf("merged spans = %v", s)
	}
}

// A held file has nothing acknowledged until it is committed, so writes
// must not wait on the window.
func TestHeldWritesDontWait(t *testing.T) {
	fake := &tustest.FakeServer{}
	cfg, coll := setup(t, fake, 1000, 1000)
	u, err := Prepare(cfg, coll, "held.bin", 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	done := make(chan struct{})
	go func() {
		for off := 0; off < 10000; off += 1000 {
			u.WriteAt(make([]byte, 1000), int64(off))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("held writes blocked")
	}
	if err := u.Commit(context.Background(), time.Unix(1710000000, 0)); err != nil {
		t.Fatal(err)
	}
	waitOK(t, u)
	if got := fake.Uploads()["/dav/spaces/s/dir/held.bin"]; got == nil || got.Meta["mtime"] != "1710000000" || !got.Done() {
		t.Fatalf("upload = %+v", got)
	}
}

// While the server finishes a fully sent file, Status says so with an
// estimate, and the time it took is learned for the next estimate.
func TestFinishEstimate(t *testing.T) {
	old := learnFrom
	learnFrom = 0
	defer func() { learnFrom = old }()
	finishRate.rate, finishRate.learned = defaultFinishRate, false

	fake := &tustest.FakeServer{Finish: 300 * time.Millisecond}
	cfg, coll := setup(t, fake, 0, 1<<20)
	const size = 1 << 20
	u, err := Start(context.Background(), cfg, coll, "big.bin", size, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	u.WriteAt(randomBytes(size), 0)
	deadline := time.Now().Add(5 * time.Second)
	for !u.Status().Waiting && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !u.Status().Waiting {
		t.Fatal("never reported waiting for the server")
	}
	waitOK(t, u)
	// learned: 1 MB in ~0.3 s, far slower than the 100 MB/s default
	if r := finishRate.rate; r > 10<<20 {
		t.Fatalf("finish rate %.0f B/s not learned", r)
	}
	if left := finishLeft(size, time.Now()); left < 200*time.Millisecond || left > time.Second {
		t.Fatalf("estimate after learning = %v", left)
	}
}
