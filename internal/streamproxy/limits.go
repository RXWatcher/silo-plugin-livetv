package streamproxy

import (
	"sync"
	"time"
)

// semaphore is a counting semaphore used to bound globally-shared resources:
// the total number of in-flight client streams and the total number of
// concurrent upstream connections. A zero/negative capacity disables the cap
// (TryAcquire always succeeds and Release is a no-op) so the proxy degrades to
// its previous unbounded behaviour when the operator leaves the knob unset.
type semaphore struct {
	cap int

	mu  sync.Mutex
	cur int
}

// newSemaphore builds a counting semaphore with the given capacity. capacity
// <= 0 means "unlimited".
func newSemaphore(capacity int) *semaphore {
	return &semaphore{cap: capacity}
}

// TryAcquire takes one slot without blocking. It returns true when a slot was
// taken (the caller must later Release) and false when the cap is already
// reached. An unlimited semaphore always returns true.
func (s *semaphore) TryAcquire() bool {
	if s == nil || s.cap <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur >= s.cap {
		return false
	}
	s.cur++
	return true
}

// Release returns one previously-acquired slot. It is safe to call on an
// unlimited semaphore (no-op) and never drops below zero.
func (s *semaphore) Release() {
	if s == nil || s.cap <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur > 0 {
		s.cur--
	}
}

// InUse reports the number of currently-held slots. Exposed for tests and
// diagnostics; callers should not gate on it (use TryAcquire instead).
func (s *semaphore) InUse() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// tokenBucket is a classic per-key token bucket. Each Allow() consumes one
// token; tokens refill continuously at refillRate per second up to burst.
// A user pinned at their concurrency cap can otherwise hammer CreateSession /
// the segment endpoints in a tight loop; the bucket throttles that to a
// sustainable rate while still permitting a short burst.
type tokenBucket struct {
	burst  float64
	rate   float64 // tokens per second
	tokens float64
	last   time.Time
}

// allow consumes a token if one is available, refilling first based on elapsed
// time. Returns true when the call is permitted.
func (b *tokenBucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.last = now
		b.tokens = b.burst
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// rateLimiter holds one tokenBucket per key (per user id). Buckets are created
// lazily and reaped opportunistically once they have refilled to full and gone
// idle, so the map can't grow without bound across a churn of distinct users.
type rateLimiter struct {
	rate  float64
	burst float64
	// idle is the duration after which a full, untouched bucket is reaped.
	idle    time.Duration
	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

// newRateLimiter builds a per-key limiter. ratePerSec <= 0 disables limiting
// (Allow always returns true). burst defaults to ratePerSec when <= 0 so a
// single positive knob yields a sensible bucket.
func newRateLimiter(ratePerSec, burst float64) *rateLimiter {
	if burst <= 0 {
		burst = ratePerSec
	}
	return &rateLimiter{
		rate:    ratePerSec,
		burst:   burst,
		idle:    time.Minute,
		buckets: make(map[string]*tokenBucket),
	}
}

// Allow reports whether the keyed caller may proceed right now, consuming a
// token when it does. An unlimited limiter (rate <= 0) always allows.
func (l *rateLimiter) Allow(key string) bool {
	if l == nil || l.rate <= 0 {
		return true
	}
	now := nowFunc()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &tokenBucket{burst: l.burst, rate: l.rate, tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	allowed := b.allow(now)
	l.reapLocked(now)
	return allowed
}

// reapLocked drops buckets untouched for longer than l.idle. Any bucket idle
// that long has, by construction, refilled to full burst (the refill rate is
// positive and l.idle is chosen to exceed a full refill), so it holds no state
// worth keeping — a fresh bucket created on the next request for that key is
// identical. Called under the lock from Allow so the map stays bounded by the
// set of recently-active users rather than all users ever seen.
func (l *rateLimiter) reapLocked(now time.Time) {
	if len(l.buckets) == 0 {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > l.idle {
			delete(l.buckets, k)
		}
	}
}
