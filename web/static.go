package main

import (
	"embed"
	"net/http"
)

//go:embed index.html
var uiFS embed.FS

// newUIServer returns the handler that serves the embedded single-page UI.
func newUIServer() http.FileSystem {
	return http.FS(uiFS)
}
