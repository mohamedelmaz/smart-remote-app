package remote

import (
	"io"
	"net/http"
	"strings"

	"smartremote/server/internal/web"
)

// webHandler serves the embedded dashboard assets.
//
// The sub-path is stripped before the lookup because embed.FS stores files
// relative to the module root, under "assets/".
func webHandler() http.Handler {
	fsys := http.FS(web.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only serve the assets directory; anything else is a 404 rather
		// than an attempt to walk out of the embedded FS.
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		f, err := fsys.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()

		seeker, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "asset is not seekable", http.StatusInternalServerError)
			return
		}
		info, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// The dashboard is small and changes with every server build, so it
		// is served with a short cache lifetime rather than a long immutable
		// one that would leave users on a stale UI.
		w.Header().Set("Cache-Control", "public, max-age=60")
		switch {
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(name, ".html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		http.ServeContent(w, r, info.Name(), info.ModTime(), seeker)
	})
}

// DashboardHandler serves the dashboard index at /.
//
// It is separate from webHandler so the root path can serve index.html while
// /assets/... serves the rest, without exposing the FS root.
func DashboardHandler() http.Handler {
	assets := web.Assets
	index, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard assets are missing from this build", http.StatusInternalServerError)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(index)
	})
}
