// Package webui exists only so the embedded single-page app can be reached
// from cmd/cliamp-web: go:embed paths are relative to the source file, and
// the Vue build output lives in webui/dist.
package webui

import "embed"

//go:embed dist
var Dist embed.FS
