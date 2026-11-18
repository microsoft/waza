package manifestaggregate

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/stretchr/testify/require"
)

var browserFixtureOutput = flag.String("aggregate-browser-fixture-output", "", "Exclusively save the actual large canonical report for the browser caller controls")

func TestLargeCanonicalPublicReportFitsAggregateProfile(t *testing.T) {
	backend, sources, _ := setup(t)
	wire, err := os.ReadFile("../artifactmanifest/testdata/publication-manifest.json")
	require.NoError(t, err)
	manifest, err := artifactmanifest.Parse(wire)
	require.NoError(t, err)
	original, err := os.ReadFile("../artifactmanifest/testdata/publication-terminal.json")
	require.NoError(t, err)
	terminal, err := runnerterminal.Validate(original, manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	manifest.Binding.GraderIDs = []string{}
	terminal.Observation.Validations = []runnerterminal.Verdict{}
	for index := range 128 {
		prefix := fmt.Sprintf("grader-%03d-", index)
		switch index {
		case 0:
			prefix = "grader-\U00010000<>&\u2028\u2029"
		case 1:
			prefix = "grader-\ue000"
		}
		name := prefix + strings.Repeat("\x01", 256-len(prefix))
		require.Len(t, name, 256)
		manifest.Binding.GraderIDs = append(manifest.Binding.GraderIDs, name)
		terminal.Observation.Validations = append(terminal.Observation.Validations, runnerterminal.Verdict{Identifier: name, Passed: true})
	}
	slices.Sort(manifest.Binding.GraderIDs)
	terminalBytes, err := runnerterminal.Encode(*terminal, manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	store := artifactmanifest.NewStore(backend)
	terminalArtifact := artifactmanifest.Artifact{
		Name: manifest.TerminalArtifact, SHA256: artifactmanifest.Hash(terminalBytes),
		SizeBytes: int64(len(terminalBytes)), MediaType: "application/json",
	}
	require.NoError(t, store.Put(t.Context(), terminalArtifact, terminalBytes))
	inputs := []Input{}
	for index := range MaxInputs {
		content := []byte(strconv.Itoa(index))
		nonce := artifactmanifest.Artifact{
			Name: "population-member", SHA256: artifactmanifest.Hash(content),
			SizeBytes: int64(len(content)), MediaType: "application/octet-stream",
		}
		require.NoError(t, store.Put(t.Context(), nonce, content))
		manifest.Artifacts = []artifactmanifest.Artifact{terminalArtifact, nonce}
		declaration, err := artifactmanifest.Encode(*manifest)
		require.NoError(t, err)
		digest, err := store.Create(t.Context(), declaration)
		require.NoError(t, err)
		_, err = store.Finalize(t.Context(), digest)
		require.NoError(t, err)
		inputs = append(inputs, Input{Manifest: digest})
	}
	report, err := Build(t.Context(), sources, inputs, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, MaxInputs, report.Counts.VerifiedManifests)
	canonical, err := Encode(*report)
	require.NoError(t, err)
	require.Greater(t, len(canonical), 16<<20)
	require.LessOrEqual(t, len(canonical), MaxReportBytes)
	t.Logf("actual canonical public report: %d bytes", len(canonical))
	_, err = Parse(canonical)
	require.NoError(t, err)
	if *browserFixtureOutput != "" {
		file, err := os.OpenFile(*browserFixtureOutput, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		written, err := file.Write(canonical)
		closeErr := file.Close()
		require.NoError(t, err)
		require.NoError(t, closeErr)
		require.Equal(t, len(canonical), written)
	}
}
