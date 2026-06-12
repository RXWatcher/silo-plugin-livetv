package server

import (
	"sync"
	"time"
)

// guideCacheNow is the clock used by guideCache; tests override it to drive
// expiry without sleeping.
var guideCacheNow = time.Now

// guideCacheEntry is one stored guide response body plus its freshness deadline.
type guideCacheEntry struct {
	body   []byte
	expiry time.Time
}

// guideCache is a tiny concurrency-safe TTL cache for assembled GET /guide
// JSON responses. The guide is read from the database (already populated by the
// XMLTV refresh worker) on every poll; clients re-poll the same windows
// frequently, so a short cache collapses repeated identical queries onto a
// single DB round-trip and JSON encode.
//
// Keying includes the user id, requested window, and channel/group selector so
// two users with different visibility never share an entry. A non-positive TTL
// disables the cache (Get always misses, Set is a no-op).
type guideCache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]guideCacheEntry
}

// newGuideCache builds a guide cache with the given freshness window. ttl <= 0
// yields a disabled cache.
func newGuideCache(ttl time.Duration) *guideCache {
	return &guideCache{ttl: ttl, entries: make(map[string]guideCacheEntry)}
}

// Get returns the cached response body for key when present and fresh.
func (c *guideCache) Get(key string) ([]byte, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if guideCacheNow().After(e.expiry) {
		delete(c.entries, key)
		return nil, false
	}
	return e.body, true
}

// Set stores body under key with the cache TTL. To keep the map bounded under
// a churn of distinct windows, expired entries are swept opportunistically on
// write once the map grows past a small threshold.
func (c *guideCache) Set(key string, body []byte) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := guideCacheNow()
	if len(c.entries) > 256 {
		for k, e := range c.entries {
			if now.After(e.expiry) {
				delete(c.entries, k)
			}
		}
	}
	c.entries[key] = guideCacheEntry{body: body, expiry: now.Add(c.ttl)}
}
