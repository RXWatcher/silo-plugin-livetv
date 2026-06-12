package streamproxy

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-livetv/internal/store"
)

// withFrozenClock swaps the package nowFunc for the duration of fn, restoring
// it afterwards. Returns a pointer the test mutates to advance time.
func withFrozenClock(t *testing.T, start time.Time, fn func(now *time.Time)) {
	t.Helper()
	cur := start
	orig := nowFunc
	nowFunc = func() time.Time { return cur }
	defer func() { nowFunc = orig }()
	fn(&cur)
}

func TestTTLCache_HitMissExpiry(t *testing.T) {
	withFrozenClock(t, time.Unix(1000, 0), func(now *time.Time) {
		c := newTTLCache[string, []byte](2 * time.Second)

		if _, ok := c.Get("a"); ok {
			t.Fatal("expected miss on empty cache")
		}
		c.Set("a", []byte("hello"))
		if v, ok := c.Get("a"); !ok || string(v) != "hello" {
			t.Fatalf("expected hit, got %q ok=%v", v, ok)
		}

		// Within TTL: still fresh.
		*now = now.Add(1 * time.Second)
		if _, ok := c.Get("a"); !ok {
			t.Fatal("expected hit within TTL")
		}

		// Past TTL: expired.
		*now = now.Add(2 * time.Second)
		if _, ok := c.Get("a"); ok {
			t.Fatal("expected miss after TTL")
		}
	})
}

func TestTTLCache_ZeroTTLDisabled(t *testing.T) {
	c := newTTLCache[string, []byte](0)
	c.Set("a", []byte("x"))
	if _, ok := c.Get("a"); ok {
		t.Fatal("zero-TTL cache must always miss")
	}
}

func TestSemaphore_CapEnforced(t *testing.T) {
	s := newSemaphore(2)
	for i := 0; i < 2; i++ {
		if !s.TryAcquire() {
			t.Fatalf("acquire %d should succeed under cap", i)
		}
	}
	if s.TryAcquire() {
		t.Fatal("expected third acquire to fail at cap")
	}
	if s.InUse() != 2 {
		t.Fatalf("InUse = %d, want 2", s.InUse())
	}
	s.Release()
	if !s.TryAcquire() {
		t.Fatal("expected acquire to succeed after release")
	}
}

func TestSemaphore_Unlimited(t *testing.T) {
	s := newSemaphore(0)
	for i := 0; i < 1000; i++ {
		if !s.TryAcquire() {
			t.Fatal("unlimited semaphore must always acquire")
		}
	}
	// Release must not panic / underflow.
	for i := 0; i < 1000; i++ {
		s.Release()
	}
}

func TestSemaphore_ReleaseNeverUnderflows(t *testing.T) {
	s := newSemaphore(1)
	s.Release() // release with nothing held
	if s.InUse() != 0 {
		t.Fatalf("InUse = %d, want 0", s.InUse())
	}
	if !s.TryAcquire() {
		t.Fatal("expected acquire to succeed")
	}
}

func TestSemaphore_Concurrent(t *testing.T) {
	const cap = 8
	s := newSemaphore(cap)
	var wg sync.WaitGroup
	var mu sync.Mutex
	acquired := 0
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.TryAcquire() {
				mu.Lock()
				acquired++
				mu.Unlock()
				s.Release()
			}
		}()
	}
	wg.Wait()
	if s.InUse() != 0 {
		t.Fatalf("InUse = %d after all released, want 0", s.InUse())
	}
}

func TestRateLimiter_BurstThenThrottle(t *testing.T) {
	withFrozenClock(t, time.Unix(2000, 0), func(now *time.Time) {
		// 5 tokens/sec, burst 3.
		l := newRateLimiter(5, 3)
		// Burst of 3 should pass.
		for i := 0; i < 3; i++ {
			if !l.Allow("user1") {
				t.Fatalf("burst token %d should be allowed", i)
			}
		}
		// 4th in the same instant should be throttled.
		if l.Allow("user1") {
			t.Fatal("expected throttle after burst exhausted")
		}
		// After 0.2s a token refills (1/5s).
		*now = now.Add(200 * time.Millisecond)
		if !l.Allow("user1") {
			t.Fatal("expected one token after refill window")
		}
		if l.Allow("user1") {
			t.Fatal("expected throttle again immediately after consuming refilled token")
		}
	})
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	withFrozenClock(t, time.Unix(3000, 0), func(_ *time.Time) {
		l := newRateLimiter(1, 1)
		if !l.Allow("a") {
			t.Fatal("user a first call should pass")
		}
		// a is now empty, but b has its own bucket.
		if !l.Allow("b") {
			t.Fatal("user b should have an independent bucket")
		}
		if l.Allow("a") {
			t.Fatal("user a should be throttled")
		}
	})
}

func TestRateLimiter_ZeroDisabled(t *testing.T) {
	l := newRateLimiter(0, 0)
	for i := 0; i < 100; i++ {
		if !l.Allow("anyone") {
			t.Fatal("zero-rate limiter must always allow")
		}
	}
}

// TestFetchPlaylistBody_CachesUpstream verifies the per-channel playlist cache
// collapses repeated polls within the TTL onto a single upstream fetch, then
// re-fetches once the entry expires. Uses an unguarded httptest client (the
// SSRF guard would reject loopback) and an empty SourceM3UID so no DB lookup is
// needed.
func TestFetchPlaylistBody_CachesUpstream(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("#EXTM3U\nseg1.ts\n"))
	}))
	defer upstream.Close()

	withFrozenClock(t, time.Unix(5000, 0), func(now *time.Time) {
		d := &Deps{
			HTTP:   upstream.Client(),
			Limits: Limits{PlaylistCacheTTL: 2 * time.Second},
		}
		ch := store.Channel{ID: "chan-1", UpstreamURL: upstream.URL}
		r := httptest.NewRequest(http.MethodGet, "/x", nil)

		b1, ok := d.fetchPlaylistBody(r, ch)
		if !ok || len(b1) == 0 {
			t.Fatal("first fetch failed")
		}
		// Second poll within TTL must be served from cache (no new upstream hit).
		if _, ok := d.fetchPlaylistBody(r, ch); !ok {
			t.Fatal("second fetch failed")
		}
		if got := hits.Load(); got != 1 {
			t.Fatalf("upstream hits = %d within TTL, want 1", got)
		}

		// Past TTL: a fresh upstream fetch.
		*now = now.Add(3 * time.Second)
		if _, ok := d.fetchPlaylistBody(r, ch); !ok {
			t.Fatal("third fetch failed")
		}
		if got := hits.Load(); got != 2 {
			t.Fatalf("upstream hits = %d after expiry, want 2", got)
		}
	})
}

// TestFetchPlaylistBody_UpstreamCapRefuses confirms the global upstream cap
// gates playlist fetches: with the only slot held, a fetch is refused.
func TestFetchPlaylistBody_UpstreamCapRefuses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer upstream.Close()

	d := &Deps{
		HTTP:   upstream.Client(),
		Limits: Limits{GlobalUpstreamCap: 1},
	}
	// Exhaust the single upstream slot.
	if !d.upstreamSemaphore().TryAcquire() {
		t.Fatal("expected to acquire the only slot")
	}
	defer d.upstreamSemaphore().Release()

	ch := store.Channel{ID: "chan-1", UpstreamURL: upstream.URL}
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, ok := d.fetchPlaylistBody(r, ch); ok {
		t.Fatal("expected fetch to be refused at upstream cap")
	}
}

func TestRateLimiter_ReapsIdleBuckets(t *testing.T) {
	withFrozenClock(t, time.Unix(4000, 0), func(now *time.Time) {
		l := newRateLimiter(5, 5)
		l.idle = time.Minute
		l.Allow("ghost") // create + consume one token
		if len(l.buckets) != 1 {
			t.Fatalf("expected 1 bucket, got %d", len(l.buckets))
		}
		// Advance past idle window so ghost's bucket refills to full and is
		// reaped on the next unrelated Allow.
		*now = now.Add(2 * time.Minute)
		l.Allow("other")
		if _, ok := l.buckets["ghost"]; ok {
			t.Fatal("expected idle ghost bucket to be reaped")
		}
	})
}
