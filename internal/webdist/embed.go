// Package webdist holds the built web UI (webui/ → npm run build writes here).
package webdist

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(files, "dist")
	return sub
}
