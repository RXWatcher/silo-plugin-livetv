package httpclient

import "net/url"

// RedactURL returns a log-safe rendering of raw with any embedded credentials
// stripped: userinfo (user:pass@) is dropped entirely and the query string is
// removed (xtream-style providers carry username/password there). The scheme,
// host, and path are preserved so logs stay diagnostically useful. Unparseable
// input is reported as "<unparseable-url>" rather than echoed.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<unparseable-url>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
