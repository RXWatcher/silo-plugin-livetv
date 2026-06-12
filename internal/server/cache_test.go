package server

import (
	"testing"
	"time"
)

func TestGuideCache_HitMissExpiry(t *testing.T) {
	cur := time.Unix(1000, 0)
	orig := guideCacheNow
	guideCacheNow = func() time.Time { return cur }
	defer func() { guideCacheNow = orig }()

	c := newGuideCache(3 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss on empty cache")
	}
	c.Set("k", []byte(`{"data":{}}`))
	if v, ok := c.Get("k"); !ok || string(v) != `{"data":{}}` {
		t.Fatalf("expected hit, got %q ok=%v", v, ok)
	}

	cur = cur.Add(2 * time.Second)
	if _, ok := c.Get("k"); !ok {
		t.Fatal("expected hit within TTL")
	}

	cur = cur.Add(2 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Fatal("expected miss after TTL")
	}
}

func TestGuideCache_ZeroTTLDisabled(t *testing.T) {
	c := newGuideCache(0)
	c.Set("k", []byte("x"))
	if _, ok := c.Get("k"); ok {
		t.Fatal("zero-TTL guide cache must always miss")
	}
}

func TestGuideCacheKey_StableAndDiscriminating(t *testing.T) {
	start := time.Unix(1000, 0).UTC()
	end := start.Add(4 * time.Hour)

	// Same inputs (channels in different order) collide.
	k1 := guideCacheKey("u1", start, end, "news", []string{"a", "b"})
	k2 := guideCacheKey("u1", start, end, "news", []string{"b", "a"})
	if k1 != k2 {
		t.Fatalf("expected channel-order-independent key, got %q vs %q", k1, k2)
	}

	// Different user => different key.
	if guideCacheKey("u2", start, end, "news", []string{"a", "b"}) == k1 {
		t.Fatal("expected different key for different user")
	}
	// Different group => different key.
	if guideCacheKey("u1", start, end, "sports", []string{"a", "b"}) == k1 {
		t.Fatal("expected different key for different group")
	}
	// Different window => different key.
	if guideCacheKey("u1", start, end.Add(time.Hour), "news", []string{"a", "b"}) == k1 {
		t.Fatal("expected different key for different window")
	}
	// Sub-second jitter on start truncated away => same key.
	jitter := start.Add(300 * time.Millisecond)
	if guideCacheKey("u1", jitter, end, "news", []string{"a", "b"}) != k1 {
		t.Fatal("expected sub-second jitter to be truncated to same key")
	}
}
