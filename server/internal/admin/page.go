package admin

import (
	_ "embed"
	"net"
	"net/http"
)

// consoleHTML is the whole console: one self-contained page, no build step and
// no bundle. It is embedded rather than served from disk so the binary stays
// the single deployable artifact the deploy script rsyncs.
//
//go:embed console.html
var consoleHTML []byte

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	// The console builds its own DOM from same-origin JSON and loads nothing
	// external, so it can afford the strictest policy going. This is the
	// backstop if a torrent name ever reaches the page unescaped.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
			"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(consoleHTML)
}

// isTrustedPeer reports whether an address belongs to the infrastructure in
// front of this process, whose forwarding headers may be believed.
func isTrustedPeer(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
