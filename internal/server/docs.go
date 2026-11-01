package server

import (
	"embed"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed swagger/*
var swagger embed.FS

func serveDocs(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/docs")
	name = strings.TrimLeft(name, "/")
	if name == "" {
		name = "index.html"
	}
	b, err := swagger.ReadFile("swagger/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(name)))
	_, _ = w.Write(b)
}
