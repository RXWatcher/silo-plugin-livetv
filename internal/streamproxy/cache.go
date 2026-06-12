package streamproxy

import (
	"sync"
	"time"
)

// nowFunc is the clock used by ttlCache. Production uses time.Now; tests swap
// it to drive expiry deterministically without sleeping.
var nowFunc = time.Now

// cacheEntry is one stored value plus the instant it stops being fresh.
type cacheEntry[V any] struct {
	val    V
	expiry time.Time
}

// ttlCache is a tiny concurrency-safe map of key -> value with a per-entry
// expiry. It is deliberately minimal: there is no background eviction goroutine
// (entries are lazily replaced on the next miss for the same key, and the
// proxy's key space is bounded by the channel count). Zero TTL disables
// caching entirely so callers can wire a single knob to turn it off.
//
// Used for the HLS playlist hot path: RewritePlaylist re-fetches upstream on
// every .m3u8 poll (every few seconds, per viewer). A 1-2s cache collapses N
// concurrent viewers of one channel into a single upstream fetch.
type ttlCache[K comparable, V any] struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[K]cacheEntry[V]
}

// newTTLCache builds a cache with the given freshness window. A non-positive
// ttl yields a cache whose Get always misses and whose Set is a no-op, so the
// caller transparently falls back to the uncached path.
func newTTLCache[K comparable, V any](ttl time.Duration) *ttlCache[K, V] {
	return &ttlCache[K, V]{
		ttl:     ttl,
		entries: make(map[K]cacheEntry[V]),
	}
}

// Get returns the cached value for key when present and still fresh. The
// second return reports whether a usable value was found.
func (c *ttlCache[K, V]) Get(key K) (V, bool) {
	var zero V
	if c == nil || c.ttl <= 0 {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	if nowFunc().After(e.expiry) {
		delete(c.entries, key)
		return zero, false
	}
	return e.val, true
}

// Set stores val under key with the cache's TTL. A non-positive TTL makes this
// a no-op so disabling the cache needs no caller-side branching.
func (c *ttlCache[K, V]) Set(key K, val V) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry[V]{val: val, expiry: nowFunc().Add(c.ttl)}
}
