package assurance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func TestVerifyFileCaseRepeatedEvidenceReferences(t *testing.T) {
	for _, test := range []struct {
		name     string
		content  string
		expected bool
	}{
		{"good", "state: ready\n", true},
		{"alternative-valid", "note: another route\nstate: ready\n", true},
		{"negative", "state: wrong\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, candidate, check, root := repeatedEvidenceVerificationFixture(t, test.content, test.expected)
			require.Len(t, check.Evidence, 64)
			seen := make(map[string]bool)
			for _, reference := range check.Evidence {
				require.False(t, seen[reference.ArtifactID])
				seen[reference.ArtifactID] = true
			}
			name := filepath.Join(root, candidate.Snapshot)
			before, err := os.ReadFile(name)
			require.NoError(t, err)
			for range 2 {
				observation := verifyFileCase(t.Context(), request, candidate, check)
				require.Equal(t, Observed, observation.State)
				require.Equal(t, "label_agreement", observation.Reason)
				require.NotNil(t, observation.Result)
				require.Equal(t, test.expected, observation.Result.Passed)
				require.Equal(t, test.expected, observation.ExpectedPassed)
				require.NotNil(t, observation.Agreement)
				require.True(t, *observation.Agreement)
				require.Equal(t, check.Evidence, observation.Evidence)
				require.NotContains(t, observation.Result.Details, "workspace_dir")
				require.Len(t, observation.Bindings, 3)
				require.Equal(t, "verified", observation.Bindings[0].State)
				require.Equal(t, "verified", observation.Bindings[1].State)
				if test.expected {
					require.Equal(t, 1.0, observation.Result.Score)
				} else {
					require.Less(t, observation.Result.Score, 1.0)
				}
			}
			after, err := os.ReadFile(name)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestVerifyFileCaseRepeatedEvidenceValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *snapshot.Snapshot, *ReferenceCase, *ReferenceCheck)
		state  ObservationState
		reason string
	}{
		{"manifest-identity", func(_ *testing.T, snap *snapshot.Snapshot, _ *ReferenceCase, _ *ReferenceCheck) {
			snap.Evidence.Artifacts[len(snap.Evidence.Artifacts)-1].Reason = "changed without sealing"
		}, Invalid, "snapshot_binding_invalid"},
		{"selected-manifest-identity", func(_ *testing.T, _ *snapshot.Snapshot, candidate *ReferenceCase, _ *ReferenceCheck) {
			candidate.ManifestSHA256 = byteSHA256([]byte("another manifest"))
		}, Invalid, "snapshot_binding_invalid"},
		{"cross-attempt-reference", func(_ *testing.T, _ *snapshot.Snapshot, _ *ReferenceCase, check *ReferenceCheck) {
			check.Evidence[len(check.Evidence)-1].Origin.AttemptCount++
		}, Invalid, "evidence_reference_invalid"},
		{"absent-artifact", func(_ *testing.T, _ *snapshot.Snapshot, _ *ReferenceCase, check *ReferenceCheck) {
			check.Evidence[len(check.Evidence)-1].ArtifactID = "workspace-file/absent.txt"
		}, Invalid, "evidence_reference_invalid"},
		{"required-reference-missing", func(_ *testing.T, _ *snapshot.Snapshot, _ *ReferenceCase, check *ReferenceCheck) {
			check.Evidence = check.Evidence[1:]
		}, InsufficientEvidence, "required_file_reference_missing"},
		{"content-digest", func(_ *testing.T, snap *snapshot.Snapshot, _ *ReferenceCase, _ *ReferenceCheck) {
			snap.WorkspaceFiles[0].Content = "tampered"
		}, InsufficientEvidence, "evidence_content_not_verified"},
		{"source-digest", func(t *testing.T, snap *snapshot.Snapshot, candidate *ReferenceCase, _ *ReferenceCheck) {
			index := -1
			for i, file := range snap.WorkspaceFiles {
				if file.Path == "state.txt" {
					index = i
				}
			}
			require.NotEqual(t, -1, index)
			snap.WorkspaceFiles[index].SHA256 = byteSHA256([]byte("another file"))
			for i := range snap.Evidence.Artifacts {
				if snap.Evidence.Artifacts[i].ID == "workspace-file/state.txt" {
					var err error
					snap.Evidence.Artifacts[i].ContentDigest, err = evidence.JSONDigest(snap.WorkspaceFiles[index])
					require.NoError(t, err)
				}
			}
			require.NoError(t, evidence.Seal(snap.Evidence))
			candidate.ManifestSHA256 = snap.Evidence.SHA256
		}, InsufficientEvidence, "required_file_not_verified"},
		{"relative-pointer-valid", func(_ *testing.T, _ *snapshot.Snapshot, _ *ReferenceCase, check *ReferenceCheck) {
			check.Evidence[0].Pointer = "/content"
		}, Observed, "label_agreement"},
		{"relative-pointer-missing", func(_ *testing.T, _ *snapshot.Snapshot, _ *ReferenceCase, check *ReferenceCheck) {
			check.Evidence[0].Pointer = "/absent"
		}, InsufficientEvidence, "evidence_content_not_verified"},
		{"document-locator-missing", func(t *testing.T, snap *snapshot.Snapshot, candidate *ReferenceCase, _ *ReferenceCheck) {
			for i := range snap.Evidence.Artifacts {
				if snap.Evidence.Artifacts[i].ID == "workspace-file/state.txt" {
					snap.Evidence.Artifacts[i].Pointer = "/workspaceFiles/64"
				}
			}
			require.NoError(t, evidence.Seal(snap.Evidence))
			candidate.ManifestSHA256 = snap.Evidence.SHA256
		}, Invalid, "evidence_locator_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, candidate, check, root := repeatedEvidenceVerificationFixture(t, "state: ready\n", true)
			name := filepath.Join(root, candidate.Snapshot)
			before, err := os.ReadFile(name)
			require.NoError(t, err)
			snap, err := snapshot.ParseSnapshot(before, "resolver regression")
			require.NoError(t, err)
			beforeInput := marshalReferenceTest(t, []any{snap, candidate, check})
			test.mutate(t, snap, &candidate, &check)
			require.NotEqual(t, beforeInput, marshalReferenceTest(t, []any{snap, candidate, check}))
			after := marshalReferenceTest(t, snap)
			require.NoError(t, os.WriteFile(name, after, 0o600))
			observation := verifyFileCase(t.Context(), request, candidate, check)
			require.Equal(t, test.state, observation.State)
			require.Equal(t, test.reason, observation.Reason)
			if test.state == Observed {
				require.NotNil(t, observation.Result)
				require.True(t, observation.Result.Passed)
				require.NotNil(t, observation.Agreement)
				require.True(t, *observation.Agreement)
			} else {
				require.Nil(t, observation.Result)
				require.Nil(t, observation.Agreement)
			}
		})
	}
}
