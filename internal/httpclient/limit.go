package httpclient

import "io"

// Body-size caps for upstream downloads. These bound how much we will read from
// an upstream before giving up, defending against a hostile or buggy provider
// streaming an unbounded body into a buffered parse.
const (
	// PlaylistMaxBytes caps M3U playlists and HLS .m3u8 documents (50 MiB).
	PlaylistMaxBytes int64 = 50 << 20
	// XMLTVMaxBytes caps XMLTV guide downloads. Guides are legitimately large
	// (full multi-day EPGs), so the ceiling is generous but still bounded
	// (512 MiB) — and applied to the compressed-or-not body the parser reads.
	XMLTVMaxBytes int64 = 512 << 20
)

// LimitBody wraps an upstream response body in an io.LimitReader so a parse can
// never read more than max bytes. The returned reader is NOT a Closer; callers
// keep closing the original resp.Body.
func LimitBody(body io.Reader, max int64) io.Reader {
	return io.LimitReader(body, max)
}
