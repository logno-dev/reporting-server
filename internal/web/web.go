package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed dist/*
var assets embed.FS

func Handler() http.Handler {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(response, request)
			return
		}
		requested := strings.TrimPrefix(path.Clean(request.URL.Path), "/")
		if requested != "." {
			if _, err := fs.Stat(dist, requested); err == nil {
				if strings.HasPrefix(requested, "assets/") {
					response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(response, request)
				return
			}
		}
		request.URL.Path = "/"
		files.ServeHTTP(response, request)
	})
}
