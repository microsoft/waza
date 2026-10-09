package schemaloader

import "errors"

var ErrExternalReference = errors.New("external schema references are unavailable offline")

// Offline blocks the compiler's default file loader as well as
// network loaders. Explicitly added local resources remain available.
type Offline struct{}

func (Offline) Load(string) (any, error) {
	return nil, ErrExternalReference
}
