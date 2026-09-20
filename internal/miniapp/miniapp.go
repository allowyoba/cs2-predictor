// Package miniapp serves the Telegram Mini App's own files.
//
// They are embedded in the binary rather than shipped to the server as
// files and mounted into the reverse proxy. That was the first design, and
// it failed twice in a row for the same reason: the page is only as
// deployed as whatever copied it there, and a release checkout that did
// not carry the directory produced a green deploy serving 404s.
//
// Embedded, the page is part of the image — the same artifact that is
// signed, verified and rolled back as one thing. There is nothing left to
// copy, nothing to mount, and a rollback takes the page back with it.
package miniapp

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var files embed.FS

// Handler serves the Mini App under prefix (e.g. "/app/").
//
// Cached briefly rather than not at all: Telegram reloads a Mini App on
// every open, and the API it calls carries its own caching — markup that
// outlives a release while the data behind it does not is the confusing
// half of that pair.
func Handler(prefix string) (http.Handler, error) {
	static, err := fs.Sub(files, "static")
	if err != nil {
		return nil, err
	}
	server := http.FileServer(http.FS(static))
	return http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		// A Mini App runs inside Telegram's WebView and is never framed by
		// anyone else; saying so costs one header and closes the
		// clickjacking question outright.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		server.ServeHTTP(w, r)
	})), nil
}
