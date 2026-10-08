package cli

import "io/fs"

// WebFS is the built dashboard. main sets it from apps/web (embed) in release builds.
var WebFS fs.FS
