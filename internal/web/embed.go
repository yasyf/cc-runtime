// Package web embeds the built single-page app served by the daemon's HTTP plane.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built SPA rooted at the dist directory. It always contains at
// least the committed placeholder index.html so a clean tree compiles; a real
// web build replaces it with hashed assets.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
