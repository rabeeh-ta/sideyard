package main

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// frontend serves the built React app. Paths that aren't files get
// index.html, so client-side routes work on reload.
func frontend(dist embed.FS) http.Handler {
	files, _ := fs.Sub(dist, "web/dist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if _, err := fs.Stat(files, name); name == "" || err != nil {
			name = "index.html"
		}
		if _, err := fs.Stat(files, "index.html"); err != nil {
			http.Error(w, "Frontend not built. For development run `npm run dev` in web/ and open http://localhost:5173.", http.StatusNotFound)
			return
		}
		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeFileFS(w, r, files, name)
	})
}
