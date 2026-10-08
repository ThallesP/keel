//go:build !embedweb

// Package web is the built dashboard; see embed.go. Without the embedweb build tag nothing is
// embedded.
package web

import "io/fs"

// Dist is nil without the embedweb build tag: `keel serve` then serves the API only.
func Dist() fs.FS { return nil }
