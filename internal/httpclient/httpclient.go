// Package httpclient builds the hardened *http.Client values the plugin uses
// for every upstream fetch (stream proxy, M3U/XMLTV refresh, segment pulls).
//
// All clients share an SSRF-guarded dialer: the custom DialContext resolves the
// target host and rejects connections to loopback, RFC1918, link-local, ULA,
// unspecified, and cloud-metadata addresses. Crucially the check runs at dial
// time against the *resolved* IP, so DNS-rebinding (a name that resolves to a
// public IP on the first lookup and a private one at connect) cannot slip a
// private destination past us.
//
// Two flavours are exposed:
//
//   - Streaming: NO Client.Timeout (live MPEG-TS / HLS segments are long-lived);
//     correctness is enforced by per-phase transport timeouts plus the request
//     context, which the proxy already cancels on client disconnect.
//   - ShortLived: a small overall timeout for refresh / probe work where the
//     whole exchange should complete quickly.
package httpclient

import (
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Phase timeouts shared by both clients. These bound each stage of the request
// independently so a stalled upstream can't pin a goroutine forever, while
// still allowing an unbounded *body* read for streaming clients.
const (
	dialTimeout           = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 30 * time.Second
	idleConnTimeout       = 90 * time.Second
	expectContinueTimeout = 1 * time.Second
)

// shortLivedTimeout is the overall Client.Timeout applied to refresh/probe
// clients. Streaming clients deliberately leave Client.Timeout unset.
const shortLivedTimeout = 60 * time.Second

// ErrBlockedAddress is returned by the guarded dialer when the resolved
// destination is a private, loopback, link-local, or metadata address.
type ErrBlockedAddress struct {
	Host string
	IP   string
}

func (e *ErrBlockedAddress) Error() string {
	return fmt.Sprintf("httpclient: blocked connection to non-public address %s (host %q)", e.IP, e.Host)
}

// guardedControl is a net.Dialer Control hook. It runs after DNS resolution,
// immediately before the socket connects, with the concrete address the kernel
// is about to dial. Rejecting here defeats DNS rebinding because the IP checked
// is exactly the IP connected to.
func guardedControl(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return &ErrBlockedAddress{Host: address, IP: host}
	}
	if !isPublicIP(ip) {
		return &ErrBlockedAddress{Host: address, IP: ip.String()}
	}
	return nil
}

// isPublicIP reports whether ip is safe to dial: a routable, public unicast
// address. Everything internal (loopback, RFC1918, CGNAT, link-local, ULA,
// unspecified, multicast) and the cloud metadata endpoint is rejected.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return false // 10.0.0.0/8
		case ip4[0] == 172 && ip4[1]&0xf0 == 16:
			return false // 172.16.0.0/12
		case ip4[0] == 192 && ip4[1] == 168:
			return false // 192.168.0.0/16
		case ip4[0] == 100 && ip4[1]&0xc0 == 64:
			return false // 100.64.0.0/10 (CGNAT)
		case ip4[0] == 169 && ip4[1] == 254:
			return false // 169.254.0.0/16 (link-local / AWS metadata)
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return false // 192.0.0.0/24 (IETF protocol assignments)
		case ip4[0] == 0:
			return false // 0.0.0.0/8
		}
		return true
	}
	// IPv6: reject ULA (fc00::/7) in addition to the stdlib checks above.
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}

// newTransport builds an *http.Transport whose dialer enforces the SSRF guard
// and applies the shared phase timeouts.
func newTransport() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   guardedControl,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: expectContinueTimeout,
	}
}

// Streaming returns a client for long-lived upstream reads (MPEG-TS pass-through
// and HLS segment pulls). It has no overall Client.Timeout — the stream proxy
// relies on the request context (cancelled on client disconnect) plus the
// transport's per-phase timeouts to bound everything but the body read.
func Streaming() *http.Client {
	return &http.Client{Transport: newTransport()}
}

// ShortLived returns a client for refresh and probe work, where the entire
// exchange (including body) should finish quickly. It carries an overall
// Client.Timeout in addition to the shared transport phase timeouts.
func ShortLived() *http.Client {
	return &http.Client{
		Transport: newTransport(),
		Timeout:   shortLivedTimeout,
	}
}
