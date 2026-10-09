package testutil

import (
	"net/url"
	"path/filepath"
	"strings"
)

// FileURL converts an absolute local fixture path to an authority-free file
// URL. The leading slash keeps a Windows drive in the path, not the hostname.
func FileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}).String()
}
