package runnerunit

import (
	"slices"

	"github.com/microsoft/waza/internal/runnerterminal"
)

// Terminal projects this invocation's report without executing again. Returned
// binding values are detached comparison context, never creator authority.
func (r Report) Terminal() (runnerterminal.Envelope, *runnerterminal.Binding) {
	envelope, original := terminal(r)
	if original == nil {
		return envelope, nil
	}
	binding := *original
	binding.GraderIDs = slices.Clone(original.GraderIDs)
	if original.Identity != nil {
		identity := *original.Identity
		binding.Identity = &identity
	}
	// The projection may alias exported Report fields. Copy those identities
	// as well so editing a returned envelope cannot alter a retained binding.
	if envelope.Identity != nil {
		identity := *envelope.Identity
		envelope.Identity = &identity
	}
	if envelope.Selection != nil {
		selection := *envelope.Selection
		envelope.Selection = &selection
	}
	return envelope, &binding
}
