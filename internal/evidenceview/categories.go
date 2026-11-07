package evidenceview

import (
	"context"
	"errors"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/manifestaggregate"
)

var ErrContextAdmission = errors.New("current evidence source context did not qualify")

type Categories struct {
	evidencestate.Inspection
	Categories    []evidencestate.CategoryEvidence `json:"categories"`
	SourceContext *manifestaggregate.SourceContext `json:"source_context,omitempty"`
}

// InspectCategories admits native facts first and shares the same reader budget
// with the separately observed source/one-hop parent context.
func InspectCategories(ctx context.Context, backend artifactmanifest.Backend, digest string) (Categories, error) {
	reader, err := manifestaggregate.NewEvidenceReader(backend)
	if err != nil {
		inspection, categories, readErr := evidencestate.InspectCategories(ctx, nil, digest)
		return Categories{Inspection: inspection, Categories: categories}, errors.Join(err, readErr)
	}
	inspection, categories, err := reader.InspectCategories(ctx, digest)
	result := Categories{Inspection: inspection, Categories: categories}
	if err != nil || inspection.Document == nil {
		return result, err
	}
	source, contextErr := reader.InspectSourceContext(ctx, *inspection.Document)
	// Preserve existing successful no-parent and undeclared-source wire bytes.
	if source != nil && (source.ParentContext != nil || contextErr != nil) {
		result.SourceContext = source
	}
	if contextErr != nil {
		result.Diagnostics = append(result.Diagnostics, artifactmanifest.Diagnostic{
			Code:    "source_context_admission_failed",
			Message: "Current source or immediate parent context did not qualify. Native category facts remain independently admitted; no lineage or assurance acceptance was established.",
		})
		return result, errors.Join(ErrContextAdmission, contextErr)
	}
	return result, nil
}
