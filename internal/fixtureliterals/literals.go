// Package fixtureliterals validates evaluator-authored, non-secret fixture identifiers.
package fixtureliterals

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxCount = 64

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._%+@-]{1,126}[A-Za-z0-9]$`)

// Validate never includes declarations in errors: an evaluator may have
// accidentally supplied sensitive data instead of a synthetic fixture value.
func Validate(values []string) error {
	if len(values) > MaxCount {
		return errors.New("non_secret_literals supports at most 64 exact fixture identifiers")
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !identifier.MatchString(value) || seen[value] {
			return errors.New("non_secret_literals requires unique 3–128 byte ASCII identifiers using letters, digits, dot, underscore, percent, plus, at or hyphen")
		}
		seen[value] = true
	}
	return nil
}

// WholeToken deliberately treats punctuation used inside credentials and
// identifiers as part of the token, not as a boundary (including /, = and -).
func WholeToken(text string, start, end int) bool {
	delimiter := func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:\"'()[]{}<>", r)
	}
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(text[:start])
		if !delimiter(r) {
			return false
		}
	}
	if end < len(text) {
		r, _ := utf8.DecodeRuneInString(text[end:])
		if !delimiter(r) {
			return false
		}
	}
	return true
}
