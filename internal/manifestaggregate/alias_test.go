package manifestaggregate

import (
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/stretchr/testify/require"
)

func TestSelectedAliasedAttemptMustMatchOriginalReservation(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "alternative-valid alias", true: "different original reservation bytes"}[mismatch], func(t *testing.T) {
			backend, sources, _ := setup(t)
			original := publish(t, backend, nil)
			store := artifactmanifest.NewStore(backend)
			inspected, err := evidencestate.Inspect(t.Context(), backend, original.Evidence)
			require.NoError(t, err)
			declaration, err := store.ReadCompleted(t.Context(), original.Manifest)
			require.NoError(t, err)
			names := []string{}
			for _, artifact := range declaration.Manifest.Artifacts {
				names = append(names, artifact.Name)
			}
			publication, err := store.ReadCompleted(t.Context(), original.Manifest, names...)
			require.NoError(t, err)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			creator, err := ledger.Reserve(t.Context(), publication.Manifest.Binding, nil)
			require.NoError(t, err)
			reserved, err := ledger.Reservation(creator)
			require.NoError(t, err)
			attempt := reserved.Attempt
			if mismatch {
				attempt.Parent = &executionattempt.Parent{ExecutionID: "exec-" + strings.Repeat("f", 64), Relationship: "retry"}
			}
			aliasBytes, err := executionattempt.EncodeAttempt(attempt)
			require.NoError(t, err)
			require.Equal(t, reserved.Attempt.ExecutionID, attempt.ExecutionID)
			require.Equal(t, reserved.Attempt.Binding, attempt.Binding)
			require.Equal(t, !mismatch, string(reserved.Bytes) == string(aliasBytes),
				"control must change canonical bytes without changing execution ID or binding")
			const alias = "custom-attempt.json"
			manifest := publication.Manifest
			manifest.Artifacts = []artifactmanifest.Artifact{}
			for _, artifact := range publication.Manifest.Artifacts {
				data := publication.ArtifactBytes[artifact.Name]
				if artifact.Name == inspected.Document.AttemptArtifact {
					artifact.Name, data = alias, aliasBytes
					artifact.SHA256, artifact.SizeBytes = artifactmanifest.Hash(data), int64(len(data))
				}
				require.NotEqual(t, "execution-attempt.json", artifact.Name, "literal dependency check must not handle this control")
				require.NoError(t, store.Put(t.Context(), artifact, data))
				manifest.Artifacts = append(manifest.Artifacts, artifact)
			}
			wire, err := artifactmanifest.Encode(manifest)
			require.NoError(t, err)
			digest, err := store.Create(t.Context(), wire)
			require.NoError(t, err)
			_, err = store.Finalize(t.Context(), digest)
			require.NoError(t, err)
			_, err = ledger.Complete(t.Context(), creator, digest)
			require.NoError(t, err, "original dependency currently admits completion without checking a custom descriptor")
			document := *inspected.Document
			document.OriginManifest, document.AttemptArtifact = digest, alias
			document.ExecutionSource = &executionattempt.Source{ExecutionID: attempt.ExecutionID, ManifestDigest: digest}
			evidence, err := evidencestate.Publish(t.Context(), backend, document)
			require.NoError(t, err, "sidecar source admission legitimately supports canonical custom artifact names")
			report, err := Build(t.Context(), sources, []Input{{Manifest: digest, Evidence: evidence.EvidenceSHA256}}, DefaultLimits())
			require.NotNil(t, report)
			if mismatch {
				require.ErrorIs(t, err, ErrConflicting, "selected descriptor must bind to actual reservation bytes, not just ID/binding")
				require.Empty(t, report.Members[0].Execution)
				require.Equal(t, "conflicting", report.Members[0].State)
			} else {
				require.NoError(t, err)
				require.Equal(t, attempt.ExecutionID, report.Members[0].Execution)
				require.Equal(t, "complete", report.State)
			}
			canonical, err := Encode(*report)
			require.NoError(t, err)
			_, err = Parse(canonical)
			require.NoError(t, err)
		})
	}
}
