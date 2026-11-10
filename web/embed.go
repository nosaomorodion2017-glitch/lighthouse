// Package web embeds the dashboard so the service ships as a single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

// Static returns the dashboard files rooted at "static/".
func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err) // only possible if the embed directive is wrong
	}
	return sub
}
