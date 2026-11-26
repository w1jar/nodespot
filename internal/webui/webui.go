// Package webui embeds the built Svelte app (internal/webui/dist) and serves
// it as a single-page application.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

const placeholder = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>nodespot</title>
<style>body{font:16px system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#222}
code{background:#eee;padding:.1em .4em;border-radius:4px}</style></head>
<body><h1>UI not built</h1><p>The nodespot web interface has not been built into this binary.
Run <code>make web</code> and rebuild. The API is available under <code>/api</code>.</p></body></html>`

// Handler serves the embedded UI.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return placeholderHandler()
	}
	return NewHandler(sub)
}

func placeholderHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(placeholder))
	})
}

// NewHandler serves files from fsys with SPA fallback to index.html. When
// fsys has no index.html it serves the "UI not built" page.
func NewHandler(fsys fs.FS) http.Handler {
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		return placeholderHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(p, "/")
		if name != "" && !hidden(name) {
			if fi, err := fs.Stat(fsys, name); err == nil && !fi.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					// index.html (also requested by name) and other unhashed files must revalidate.
					w.Header().Set("Cache-Control", "no-cache")
				}
				serveFile(w, r, fsys, name)
				return
			}
		}
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		serveFile(w, r, fsys, "index.html")
	})
}

func hidden(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	f, err := fsys.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if rs, ok := f.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	}); ok {
		http.ServeContent(w, r, name, fi.ModTime(), rs)
		return
	}
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, fi.ModTime(), strings.NewReader(string(b)))
}
