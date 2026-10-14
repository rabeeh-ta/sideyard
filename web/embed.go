// Package web embeds the built dashboard. Run `npm run build` in this
// directory (or `make web`) before building Sideyard.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func FS() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
