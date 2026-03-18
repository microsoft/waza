// Package testutil provides shared test helpers.
package testutil

import "regexp"

// tokenCountPat matches numbers in token-count contexts across CLI output
// formats (dev loop summary boxes, check/count command tables, and
// exceeded-file detail lines). Numbers outside these contexts are left
// untouched.
var tokenCountPat = regexp.MustCompile(
	// "Tokens: 55" plus trailing horizontal whitespace (dev loop)
	`Tokens: \d+[^\S\n]*` +
		// "(55 tokens" (dev loop complexity / check detail)
		`|` + `\(\d+ tokens` +
		// "(385 < 500)" (dev loop TOKEN STATUS budget line)
		`|` + `\(\d+ [<>] \d+\)` +
		// check table data rows: two numbers before a status icon
		`|` + `\d+\s+\d+\s+[✅❌]` +
		// count table data rows: three consecutive numbers (Tokens, Chars, Lines)
		`|` + `\d+\s+\d+\s+\d+` +
		// "424 tokens (324 over limit of 100)" (check exceeded detail)
		`|` + `\d+ tokens \(\d+ over limit of \d+\)` +
		// "3/4 files" (check summary)
		`|` + `\d+/\d+ files` +
		// "1 file(s)" (check/count summary)
		`|` + `\d+ file\(s\)`,
)

var digitPat = regexp.MustCompile(`\d+`)

// StripTokenCounts replaces token-count numbers (and related derived values)
// with a fixed placeholder so format assertions don't depend on exact counts.
func StripTokenCounts(s string) string {
	return tokenCountPat.ReplaceAllStringFunc(s, func(m string) string {
		return digitPat.ReplaceAllString(m, "·")
	})
}
