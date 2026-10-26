// Package web holds the embedded templates and static assets.
package web

import "embed"

//go:embed templates static
var FS embed.FS
