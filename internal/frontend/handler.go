// Package frontend serves the frontend build without exposing directory listings.
package frontend

import (
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// New accepts the embedded build directory. Missing build assets fail at startup.
func New(assets fs.FS) (http.Handler, error) {
	if _, err := fs.ReadFile(assets, "index.html"); err != nil {
		return nil, fmt.Errorf("frontend build is missing: run make web: %w", err)
	}
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			files.ServeHTTP(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		// A missing script/font must remain a 404, not an HTML success response.
		if path.Ext(name) != "" || strings.HasPrefix(name, "assets/") || (err == nil && info.IsDir()) {
			http.NotFound(w, r)
			return
		}
		request := r.Clone(r.Context())
		url := *r.URL
		url.Path = "/"
		request.URL = &url
		files.ServeHTTP(w, request)
	}), nil
}
