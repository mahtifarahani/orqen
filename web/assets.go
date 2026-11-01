package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
)

//go:embed all:dist
var bundled embed.FS

const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func ServeUI(w http.ResponseWriter, r *http.Request, dev bool) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	var assets fs.FS
	if dev {
		assets = os.DirFS("web/dist")
	} else {
		assets, _ = fs.Sub(bundled, "dist")
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		http.Error(w, "UI not built: run make web-build", 503)
		return
	}
	file := strings.TrimLeft(r.URL.Path, "/")
	valid := fs.ValidPath(file)
	b, err := fs.ReadFile(assets, file)
	if !valid || err != nil {
		if file != "" && strings.Contains(path.Base(file), ".") {
			http.NotFound(w, r)
			return
		}
		file = "index.html"
		b, err = fs.ReadFile(assets, file)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	media := mime.TypeByExtension(path.Ext(file))
	if media == "" {
		media = "application/octet-stream"
	}
	w.Header().Set("Content-Type", media)
	cache := "no-cache"
	if strings.HasPrefix(file, "assets/") {
		cache = "public, max-age=31536000, immutable"
	}
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != "HEAD" {
		_, _ = w.Write(b)
	}
}
