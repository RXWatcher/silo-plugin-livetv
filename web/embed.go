// Package web embeds the built SPA from web/dist. The embed sits in web/ so
// it can reach the sibling dist/ directory; //go:embed is constrained to the
// package directory and its descendants.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FSEmbed returns the embedded SPA as an fs.FS rooted at dist/.
func FSEmbed() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: " + err.Error())
	}
	return sub
}

// FS returns the SPA file system rooted at dist/.
func FS() http.FileSystem { return http.FS(FSEmbed()) }

// SPAHandler returns an http.Handler that serves the embedded SPA. For paths
// that don't match a real file, it serves index.html so client-side routing
// works.
func SPAHandler() http.Handler {
	fileSrv := http.FileServer(FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSPARequest(r.URL.Path) {
			serveIndex(w, r)
			return
		}
		f, err := FS().Open(r.URL.Path)
		if err != nil {
			r.URL.Path = "/"
		} else {
			_ = f.Close()
		}
		fileSrv.ServeHTTP(w, r)
	})
}

// isSPARequest treats every non-asset path as an SPA route. Asset paths live
// under /assets/ (vite default); everything else (/, /guide, /watch/:id,
// /admin/*) should hand the browser index.html so react-router can take over.
func isSPARequest(path string) bool {
	if path == "/" {
		return true
	}
	if strings.HasPrefix(path, "/assets/") {
		return false
	}
	// Anything with a file extension is treated as a real asset.
	if dot := strings.LastIndex(path, "."); dot > strings.LastIndex(path, "/") {
		return false
	}
	return true
}

func serveIndex(w http.ResponseWriter, _ *http.Request) {
	data, err := fs.ReadFile(FSEmbed(), "index.html")
	if err != nil {
		http.Error(w, "spa not available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
