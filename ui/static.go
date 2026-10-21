package ui

import (
	"io/fs"
	"net/http"
	"strings"
)

// StaticHandler returns an http.HandlerFunc that serves static files from
// dir under the given URL path prefix (e.g. "/static/").
func StaticHandler(prefix, dir string) http.HandlerFunc {
	return staticHandler(prefix, http.Dir(dir))
}

// StaticHandlerFS returns an http.HandlerFunc that serves static files from
// the given fs.FS under the given URL path prefix. This is useful for serving
// files embedded via Go's embed package.
func StaticHandlerFS(prefix string, fsys fs.FS) http.HandlerFunc {
	return staticHandler(prefix, http.FS(fsys))
}

func staticHandler(prefix string, root http.FileSystem) http.HandlerFunc {
	fileServer := http.FileServer(root)

	var handler http.Handler
	if prefix != "" {
		handler = http.StripPrefix(prefix, fileServer)
	} else {
		handler = fileServer
	}

	return func(w http.ResponseWriter, r *http.Request) {
		// Block directory listings, including the root the prefix maps to.
		if strings.HasSuffix(r.URL.Path, "/") ||
			(prefix != "" && r.URL.Path == strings.TrimSuffix(prefix, "/")) {
			http.NotFound(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	}
}
