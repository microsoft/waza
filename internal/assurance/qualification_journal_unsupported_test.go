//go:build !linux && !darwin

package assurance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationLocalBackendUnsupported(t *testing.T) {
	_, err := qualificationOpenLocalJournal(t.Context(), qualificationLocalRegistryConfig{})
	require.ErrorContains(t, err, "unsupported")
	_, err = (qualificationOSJournalIO{}).Lock(t.Context(), nil)
	require.Error(t, err)
	_, err = qualificationSecureRead(t.Context(), nil, "file", 1)
	require.Error(t, err)
}
