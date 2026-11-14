package inventory

import (
	"fmt"

	"github.com/microsoft/waza/internal/provenance"
)

// BindingError carries the inventory failure code into provenance reports.
type BindingError struct {
	Code    string
	Message string
}

func (e *BindingError) Error() string       { return e.Code + ": " + e.Message }
func (e *BindingError) BindingCode() string { return e.Code }

// VerifyProvenanceBinding verifies a component-inventory binding: the member
// must be a runner image inventory bound to the signed runner image digest and
// repository. Plugin inventories are verified separately against the plugin
// artifact because the statement does not carry plugin digests.
func VerifyProvenanceBinding(b provenance.Binding, data []byte, s *provenance.Statement) error {
	if b.MediaType != MediaType && b.MediaType != "application/json" {
		return &BindingError{Code: "inventory_media_type_invalid", Message: fmt.Sprintf("media type %q is not %s", b.MediaType, MediaType)}
	}
	image := s.Predicate.Runner.Image
	report := Verify(data, Expectation{Kind: SubjectImage, Digest: image.Digest, Name: ImageRepository(image.Reference)})
	if failure, failed := report.FirstFailure(); failed {
		return &BindingError{Code: failure.Code, Message: failure.Message}
	}
	return nil
}

func init() {
	if err := provenance.RegisterBindingVerifier(BindingKind, provenance.BindingVerifierFunc(VerifyProvenanceBinding)); err != nil {
		panic(err)
	}
}
