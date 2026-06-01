// Package web embeds the built single-page application so the whole product
// ships as one binary. The dist/ directory is produced by `npm run build` in
// this folder; a placeholder is committed so the Go build always succeeds.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded SPA file system rooted at dist/.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
