package assurance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func qualificationLockedFixture(t *testing.T) (qualificationSourceFixture, string, *models.Lockfile) {
	t.Helper()
	_, fixture := qualificationProtocolFixture(t)
	cache := t.TempDir()
	t.Setenv("WAZA_MODULE_CACHE", cache)
	ref := "github.com/example/qualification/check.yaml@v1"
	commit := strings.Repeat("a", 40)
	module := filepath.Join(cache, "github.com", "example", "qualification", commit)
	require.NoError(t, os.MkdirAll(module, 0o700))
	preset := []byte("name: cached\ntype: prompt\nconfig:\n  rubric: rubric.md\n  mode: independent\n")
	require.NoError(t, os.WriteFile(filepath.Join(module, "check.yaml"), preset, 0o600))
	digest, err := registry.DigestDirectory(module)
	require.NoError(t, err)
	lock := models.NewLockfile()
	lock.UpsertGrader(models.LockfileGrader{
		Ref: ref, Commit: commit, Digest: digest, URL: "https://not-fetched.invalid/qualification.git",
	})
	require.NoError(t, models.WriteLockfile(filepath.Join(fixture.root, models.LockfileName), lock))
	parameters, ok := fixture.request.Spec.Graders[0].Parameters.(models.PromptGraderParameters)
	require.True(t, ok)
	parameters.Prompt = "reviewed local prompt override"
	fixture.request.Spec.Graders[0].Parameters = parameters
	source := *fixture.request.Spec
	original := source.Graders[0]
	source.Graders = []models.GraderConfig{{
		Identifier: original.Identifier, Weight: original.Weight, Ref: ref,
		Parameters: models.PromptGraderParameters{Prompt: parameters.Prompt},
	}}
	data, err := yaml.Marshal(source)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), data, 0o600))
	fixture.request.EvalSource = bytes.Clone(data)
	fixture.document.EvalSourceSHA256 = byteSHA256(data)
	calibrationRebind(t, &fixture.request, &fixture.document)
	qualificationSaveLockedLabels(t, fixture)
	return fixture, module, lock
}

func qualificationSaveLockedLabels(t *testing.T, fixture qualificationSourceFixture) {
	t.Helper()
	labels := marshalReferenceTest(t, fixture.document)
	review := marshalReferenceTest(t, ReviewDocument{
		SchemaVersion: ReferenceVersion, Kind: ReviewKind, SourceID: fixture.request.Review.SourceID,
		SubjectID: fixture.document.ID, SubjectVersion: fixture.document.Version, LabelsSHA256: byteSHA256(labels),
		State: ReviewReviewed, Reviewer: "synthetic-test-reviewer", ReviewedAt: fixture.request.Review.Decision.ReviewedAt,
	})
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "labels.json"), labels, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "review.json"), append(review, '\n'), 0o600))
}

func TestQualificationLockedPresetRetainsAdmittedExpansionThroughManifest(t *testing.T) {
	fixture, module, _ := qualificationLockedFixture(t)
	original := bytes.Clone(fixture.request.EvalSource)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	retained, err := sources.source("eval")
	require.NoError(t, err)
	require.Equal(t, original, retained)
	require.Contains(t, string(retained), "ref:")
	require.NotContains(t, sources.materializedEval, "ref:")
	require.Contains(t, sources.materializedEval, "reviewed local prompt override")
	association, arm := qualificationTestArm(t, sources, fixture)
	require.Equal(t, fixture.document.EvalResolvedConfigSHA256, arm.SourceInventory.ResolvedSpecSHA256)
	currentness, backend := qualificationTestPolicies(t)
	input, err := qualificationBuildInputs(sources, marshalReferenceTest(t, association), marshalReferenceTest(t, arm),
		fixture.request.Acceptance, fixture.request.Now, currentness, backend)
	require.NoError(t, err)
	// Recomputing jobs must use acquired bytes, not mutable originals or cache.
	require.NoError(t, os.Remove(filepath.Join(fixture.root, "eval.yaml")))
	require.NoError(t, os.Remove(filepath.Join(fixture.root, models.LockfileName)))
	require.NoError(t, os.Remove(filepath.Join(module, "check.yaml")))
	admitted, err := qualificationAdmitInputContext(t.Context(), input.bytes(), sources)
	require.NoError(t, err)
	jobs, err := qualificationDecode[[]qualificationJobWire](admitted.jobs.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	require.Len(t, jobs, 4)
	wire, err := qualificationDecode[qualificationInputs](input.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	manifestDocument, err := qualificationSeal(qualificationManifestWire{
		Kind: "waza.qualification-manifest", Version: qualificationVersion, Profile: qualificationProfile,
		InvocationID: "synthetic-locked-invocation", InputsSHA256: input.sha256(), Inputs: wire,
		Plan: *fixture.document.Calibration, Jobs: jobs,
	})
	require.NoError(t, err)
	_, err = qualificationParseManifest(manifestDocument.bytes(), admitted)
	require.NoError(t, err)
	changed := sources
	changed.materializedEval = strings.Replace(sources.materializedEval, "rubric.md", "changed.md", 1)
	require.NotEqual(t, sources.materializedEval, changed.materializedEval)
	_, err = qualificationAdmitInputContext(t.Context(), input.bytes(), changed)
	require.Error(t, err, "retained expansion must match the acquired resolved-spec binding")
	_, err = qualificationRetainedEval(sources, []byte("name: replaced\n"), "replaced")
	require.ErrorContains(t, err, "original eval source binding")
}

func TestQualificationLockedPresetOfflineAdmissionFailures(t *testing.T) {
	for _, failure := range []string{
		"missing lock", "malformed lock", "invalid lock", "missing entry", "missing cache", "digest mismatch", "malformed preset",
		"untrusted program", "external schema", "kind mismatch", "declaration binding", "resolved binding", "source binding", "canceled",
	} {
		t.Run(failure, func(t *testing.T) {
			fixture, module, lock := qualificationLockedFixture(t)
			ctx := t.Context()
			switch failure {
			case "missing lock":
				require.NoError(t, os.Remove(filepath.Join(fixture.root, models.LockfileName)))
			case "invalid lock":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, models.LockfileName), []byte("schema_version: 99\n"), 0o600))
			case "malformed lock":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, models.LockfileName), []byte("graders: [\n"), 0o600))
			case "missing entry":
				lock.Graders = nil
				require.NoError(t, models.WriteLockfile(filepath.Join(fixture.root, models.LockfileName), lock))
			case "missing cache":
				require.NoError(t, os.Remove(filepath.Join(module, "check.yaml")))
			case "digest mismatch":
				require.NoError(t, os.WriteFile(filepath.Join(module, "check.yaml"), []byte("changed"), 0o600))
			case "untrusted program", "external schema", "malformed preset":
				preset := []byte("name: cached\ntype: program\nconfig:\n  command: must-not-execute\n")
				switch failure {
				case "external schema":
					preset = []byte("name: cached\ntype: tool_constraint\nconfig:\n  expect_tools:\n    - tool: read\n      args:\n        value:\n          json_schema: {$ref: 'https://not-fetched.invalid/schema'}\n")
				case "malformed preset":
					preset = []byte("type: [\n")
				}
				require.NoError(t, os.WriteFile(filepath.Join(module, "check.yaml"), preset, 0o600))
				digest, err := registry.DigestDirectory(module)
				require.NoError(t, err)
				lock.Graders[0].Digest = digest
				require.NoError(t, models.WriteLockfile(filepath.Join(fixture.root, models.LockfileName), lock))
			case "kind mismatch":
				data := bytes.Replace(fixture.request.EvalSource, []byte("ref:"), []byte("type: text\n      ref:"), 1)
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), data, 0o600))
			case "declaration binding":
				fixture.document.Cases[0].Checks[0].GraderDeclarationSHA256 = strings.Repeat("b", 64)
				qualificationSaveLockedLabels(t, fixture)
			case "resolved binding":
				fixture.document.EvalResolvedConfigSHA256 = strings.Repeat("b", 64)
				qualificationSaveLockedLabels(t, fixture)
			case "source binding":
				fixture.document.EvalSourceSHA256 = strings.Repeat("b", 64)
				qualificationSaveLockedLabels(t, fixture)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			sources, err := qualificationAcquireSources(ctx, fixture.reads)
			require.Error(t, err)
			if failure == "untrusted program" {
				require.True(t, registry.IsProgramGraderTrustError(err), "%v", err)
			}
			if failure == "external schema" {
				require.ErrorContains(t, err, schemaloader.ErrExternalReference.Error())
			}
			require.Empty(t, sources.native.canonical)
			require.Empty(t, sources.materializedEval)
		})
	}

}

func TestQualificationLockedRefAllowanceIsOnlyDirectEvalGraders(t *testing.T) {
	require.NoError(t, qualificationSourceYAML([]byte("graders:\n  - name: check\n    ref: github.com/example/check.yaml@v1\n"), true))
	for _, source := range []string{
		"ref: external\n", "graders:\n  - config:\n      ref: external\n",
		"config:\n  ref: external\n", "graders:\n  - config:\n      schema:\n        $ref: https://not-fetched.invalid/schema\n",
		"inputs:\n  prompt_file: outside.txt\n", "include: outside.yaml\n",
	} {
		require.Error(t, qualificationSourceYAML([]byte(source), true), source)
	}
	require.Error(t, qualificationNoIncludes([]byte("graders:\n  - ref: external\n")), "ordinary private YAML policy is unchanged")
}
