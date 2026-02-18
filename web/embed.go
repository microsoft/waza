// Package web provides the embedded SPA assets for the waza dashboard.
package web

import "embed"

//go:embed all:dist
var Assets embed.FS
