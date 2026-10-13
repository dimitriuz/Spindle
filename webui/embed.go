package webui

// The web interface: three embedded files, no build step. app_test.js sits
// beside them but is not embedded. This is the vanilla port of the MiSTer
// Subsonic remote; the earlier Vue app lives in archive/webui-vue (frozen —
// new features land here only).

import (
	"embed"
	"io/fs"
)

//go:embed index.html app.js app.css
var webFS embed.FS

// FS returns the embedded page rooted at index.html.
func FS() fs.FS { return webFS }
