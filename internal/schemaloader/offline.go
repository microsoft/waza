package schemaloader

import (
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var ErrExternalReference = errors.New("external schema references are unavailable offline")

// Offline blocks the compiler's default file loader as well as
// network loaders. Explicitly added local resources remain available.
type Offline struct{}

func (Offline) Load(string) (any, error) {
	return nil, ErrExternalReference
}

// ClassifyError preserves both invalid configuration and external prerequisites
// across aggregates. LoadURLError carries its cause without implementing Unwrap.
func ClassifyError(err error) (invalid, external bool) {
	if err == nil {
		return false, false
	}
	switch e := err.(type) {
	case *jsonschema.LoadURLError:
		if e.Err != nil {
			return ClassifyError(e.Err)
		}
	case interface{ Unwrap() []error }:
		for _, child := range e.Unwrap() {
			childInvalid, childExternal := ClassifyError(child)
			invalid = invalid || childInvalid
			external = external || childExternal
		}
		if invalid || external {
			return invalid, external
		}
	case interface{ Unwrap() error }:
		if child := e.Unwrap(); child != nil {
			return ClassifyError(child)
		}
	}
	external = errors.Is(err, ErrExternalReference)
	return !external, external
}
