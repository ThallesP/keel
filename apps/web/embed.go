//go:build embedweb

// Package web is the built dashboard (apps/web/dist), embedded into the keel binary when built
// with -tags embedweb (the release image is; see the root Dockerfile). Plain builds embed
// nothing, so Go builds and tests never need bun: in dev, Vite serves the dashboard and proxies
// the API to `keel serve`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the contents of apps/web/dist (index.html at its root).
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // "dist" is a valid path: fs.Sub cannot fail on it
	}
	return sub
}
