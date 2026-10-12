//go:build !embedweb

package web

import "io/fs"

func Dist() fs.FS { return nil }
