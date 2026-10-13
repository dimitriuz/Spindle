package webui2

// The parallel "next" UI: three embedded files, no build step, served under
// /next/ beside the Vue app. Ported from the MiSTer Subsonic web remote.

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js app.css
var webFS embed.FS

// FS returns the embedded page rooted at index.html.
func FS() fs.FS { return webFS }
