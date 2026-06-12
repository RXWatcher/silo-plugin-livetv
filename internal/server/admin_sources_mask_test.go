package server

import "testing"

func TestMaskURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// userinfo with password.
		{"http://user:secret@host.example/path", "http://user:REDACTED@host.example/path"},
		// userinfo with only a username (xtream sometimes embeds the token here).
		{"http://tokenuser@host.example/path", "http://REDACTED@host.example/path"},
		// xtream-style query creds.
		{"https://host.example/get.php?username=u&password=p&type=m3u",
			"https://host.example/get.php?password=REDACTED&type=m3u&username=REDACTED"},
		// nothing sensitive → unchanged.
		{"https://host.example/playlist.m3u?type=m3u_plus", "https://host.example/playlist.m3u?type=m3u_plus"},
	}
	for _, c := range cases {
		if got := maskURL(c.in); got != c.want {
			t.Errorf("maskURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskHeaders(t *testing.T) {
	in := map[string]string{
		"User-Agent":    "livetv/1.0",
		"Authorization": "Bearer abc123",
		"Cookie":        "session=xyz",
		"X-Api-Key":     "k",
	}
	out := maskHeaders(in)
	if out["User-Agent"] != "livetv/1.0" {
		t.Errorf("User-Agent should be untouched, got %q", out["User-Agent"])
	}
	for _, k := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if out[k] != maskedValue {
			t.Errorf("%s = %q, want masked", k, out[k])
		}
	}
	// Original map must not be mutated.
	if in["Authorization"] != "Bearer abc123" {
		t.Error("maskHeaders mutated the input map")
	}
}

func TestMaskHeadersNilReturnsNonNil(t *testing.T) {
	if got := maskHeaders(nil); got == nil {
		t.Fatal("maskHeaders(nil) must return a non-nil empty map for stable JSON shape")
	}
}
