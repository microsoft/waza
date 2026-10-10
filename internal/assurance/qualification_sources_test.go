package assurance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type qualificationSourceFixture struct {
	reads    []qualificationSourceRead
	request  VerifyRequest
	document ReferenceDocument
	root     string
}

func qualificationSourceFixtureForTest(t *testing.T) qualificationSourceFixture {
	t.Helper()
	request, document, root := authoredVerificationFixture(t)
	request.Spec.SchemaVersion = "1.0"
	request.Spec.Tasks = []string{"task.yaml"}
	request.Spec.Config.TrialsPerTask = 1
	request.Spec.Config.TimeoutSec = 60
	request.Spec.Config.EngineType = "mock"
	evalBytes, err := yaml.Marshal(request.Spec)
	require.NoError(t, err)
	taskBytes, err := yaml.Marshal(request.Tasks["task"])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "eval.yaml"), evalBytes, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "task.yaml"), taskBytes, 0o600))
	request.Spec, err = models.LoadEvalSpecOffline(filepath.Join(root, "eval.yaml"))
	require.NoError(t, err)
	task, err := models.LoadTestCaseOffline(filepath.Join(root, "task.yaml"))
	require.NoError(t, err)
	request.Tasks = map[string]*models.TestCase{"task": task}
	request.EvalSource = evalBytes
	document.EvalSourceSHA256 = byteSHA256(evalBytes)
	executable := []byte("inert synthetic executable bytes — never run")
	document.ImplementationExecutableSHA256 = new(byteSHA256(executable))
	calibrationRebind(t, &request, &document)
	labels := marshalReferenceTest(t, document)
	review := marshalReferenceTest(t, ReviewDocument{
		SchemaVersion: ReferenceVersion, Kind: ReviewKind, SourceID: request.Review.SourceID,
		SubjectID: document.ID, SubjectVersion: document.Version, LabelsSHA256: byteSHA256(labels),
		State: ReviewReviewed, Reviewer: "synthetic-test-reviewer", ReviewedAt: request.Review.Decision.ReviewedAt,
	})
	for name, data := range map[string][]byte{"labels.json": labels, "review.json": append(review, '\n'), "executable.bin": executable} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), data, 0o600))
	}
	reads := []qualificationSourceRead{
		{ID: "eval", Role: "eval", Root: request.SnapshotRoot, Path: "eval.yaml"},
		{ID: "labels", Role: "references", Root: request.SnapshotRoot, Path: "labels.json"},
		{ID: "review", Role: "review", Root: request.SnapshotRoot, Path: "review.json"},
		{ID: "executable", Role: "implementation_executable", Root: request.SnapshotRoot, Path: "executable.bin"},
		{ID: "task", Role: "task", TaskID: "task", Root: request.SnapshotRoot, Path: "task.yaml"},
	}
	for _, candidate := range document.Cases {
		for _, check := range candidate.Checks {
			selector := qualificationSelector{"arm", candidate.TaskID, check.RequirementID,
				qualificationCheck{check.Check.Scope, check.Check.AfterTurn, check.Check.Grader}, candidate.ID}
			reads = append(reads, qualificationSourceRead{ID: "authored-" + candidate.ID, Role: "authored_input",
				TaskID: candidate.TaskID, Selector: &selector, Root: request.SnapshotRoot, Path: candidate.AuthoredInput.Path})
		}
	}
	return qualificationSourceFixture{reads, request, document, root}
}

func TestQualificationAcquiresActualSnapshotAndOriginalReview(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	require.NotEmpty(t, sources.native.canonical)
	original, err := os.ReadFile(filepath.Join(fixture.root, "review.json"))
	require.NoError(t, err)
	retained, err := sources.source("review")
	require.NoError(t, err)
	require.Equal(t, original, retained)
	require.True(t, strings.HasSuffix(string(retained), "\n"))
	retained[0] = '!'
	retainedAgain, err := sources.source("review")
	require.NoError(t, err)
	require.Equal(t, original, retainedAgain)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "review.json"), []byte("changed"), 0o600))
	retainedAgain, err = sources.source("review")
	require.NoError(t, err)
	require.Equal(t, original, retainedAgain, "no reopening mutable originals")
	_, err = ReadDocument(t.Context(), fixture.request.SnapshotRoot, "task.yaml", maxSnapshotBytes)
	require.NoError(t, err, "borrowed root remains caller-owned")
	_, err = sources.source("missing")
	require.Error(t, err)
	_, err = (qualificationSources{}).source("review")
	require.Error(t, err)
	parsed, err := qualificationParseSources(sources.document.bytes())
	require.NoError(t, err)
	require.Empty(t, parsed.native.canonical, "supplied source metadata is not proof of actual rooted task selection")
}

func TestQualificationSourceAcquisitionRejectsUnsafeAndUnselectedInputs(t *testing.T) {
	for _, failure := range []string{"nil root", "absolute", "traversal", "missing", "directory", "duplicate",
		"wrong task ID", "unselected path", "missing selected task", "glob", "tasks_from", "prompt_file", "unknown",
		"invalid UTF8", "includes", "task include", "canceled", "source too large", "zero count"} {
		t.Run(failure, func(t *testing.T) {
			fixture := qualificationSourceFixtureForTest(t)
			ctx := t.Context()
			switch failure {
			case "nil root":
				fixture.reads[0].Root = nil
			case "absolute":
				fixture.reads[0].Path = filepath.Join(fixture.root, "eval.yaml")
			case "traversal":
				fixture.reads[0].Path = "../eval.yaml"
			case "missing":
				fixture.reads[0].Path = "missing.yaml"
			case "directory":
				fixture.reads[0].Path = "."
			case "duplicate":
				fixture.reads = append(fixture.reads, fixture.reads[0])
			case "wrong task ID":
				fixture.reads[4].TaskID = "different"
			case "unselected path":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "other.yaml"), []byte("id: task\ninputs:\n  prompt: test\n"), 0o600))
				fixture.reads[4].Path = "other.yaml"
			case "missing selected task":
				fixture.reads = fixture.reads[:4]
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "source too large":
				require.NoError(t, os.Truncate(filepath.Join(fixture.root, "review.json"), MaxLabelBytes+1))
			case "zero count":
				fixture.reads = nil
			default:
				eval, err := os.ReadFile(filepath.Join(fixture.root, "eval.yaml"))
				require.NoError(t, err)
				switch failure {
				case "glob":
					eval = []byte(strings.ReplaceAll(string(eval), "task.yaml", "*.yaml"))
				case "tasks_from":
					eval = append(eval, []byte("\ntasks_from: outside.jsonl\n")...)
				case "unknown":
					eval = append(eval, []byte("\nunrecognized: yes\n")...)
				case "invalid UTF8":
					eval = append(eval, 0xff)
				case "includes":
					eval = append(eval, []byte("\ninclude: outside.yaml\n")...)
				case "prompt_file":
					require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "task.yaml"), []byte("id: task\ninputs:\n  prompt_file: outside.txt\n"), 0o600))
				case "task include":
					require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "task.yaml"), []byte("id: task\ninputs:\n  prompt: test\ninclude: outside\n"), 0o600))
				}
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), eval, 0o600))
			}
			_, err := qualificationAcquireSources(ctx, fixture.reads)
			require.Error(t, err)
		})
	}
}

func TestQualificationSourceParserLimitsAndScopedRoleRules(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	original, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	require.NoError(t, err)
	for _, failure := range []string{"version", "kind", "missing role", "order", "bad role", "bad digest", "bad length", "bad base64", "empty singleton", "task duplicate", "task selector", "scoped source missing selector"} {
		t.Run(failure, func(t *testing.T) {
			set, err := calibrationJSONCopy(original)
			require.NoError(t, err)
			switch failure {
			case "version":
				set.Version = "2.0"
			case "kind":
				set.Kind = "other"
			case "missing role":
				set.Sources = slices.DeleteFunc(set.Sources, func(source qualificationSource) bool { return source.Role == "eval" })
			case "order":
				set.Sources[0], set.Sources[1] = set.Sources[1], set.Sources[0]
			case "bad role":
				set.Sources[0].Role = "unknown"
			case "bad digest":
				set.Sources[0].SHA256 = strings.Repeat("0", 64)
			case "bad length":
				set.Sources[0].ByteLength++
			case "bad base64":
				set.Sources[0].BytesBase64 = "AB=="
			case "empty singleton":
				set.Sources[0].BytesBase64, set.Sources[0].ByteLength = "", 0
			case "task duplicate":
				last := set.Sources[len(set.Sources)-1]
				last.ID = "zzduplicate"
				set.Sources = append(set.Sources, last)
			case "task selector":
				set.Sources[len(set.Sources)-1].Selector = &qualificationSelector{}
			case "scoped source missing selector":
				set.Sources = append(set.Sources, qualificationSource{ID: "zzrubric", Role: "rubric"})
			}

			_, err = qualificationParseSources(marshalReferenceTest(t, set))
			require.Error(t, err)
		})
	}
	selector := qualificationSelector{"arm", "task", fixture.document.Cases[0].Checks[0].RequirementID,
		qualificationCheck{Scope: "eval", Grader: fixture.document.Cases[0].Checks[0].Check.Grader}, fixture.document.Cases[0].ID}
	source := qualificationSource{"zzrubric", "rubric", "task", &selector, 2, byteSHA256([]byte("é")), base64.StdEncoding.EncodeToString([]byte("é"))}
	original.Sources = append(original.Sources, source)
	_, err = qualificationParseSources(marshalReferenceTest(t, original))
	require.NoError(t, err)
	source.Selector.Check.AfterTurn = 1
	original.Sources[len(original.Sources)-1] = source
	_, err = qualificationParseSources(marshalReferenceTest(t, original))
	require.Error(t, err)
	_, err = qualificationReadSource(t.Context(), fixture.reads[0], 1)
	require.Error(t, err)
}

func TestQualificationSourceParserPreflightsBeforeMaterialization(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	original, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	require.NoError(t, err)
	roleLimits := map[string]uint64{}
	total := uint64(0)
	for _, source := range original.Sources {
		roleLimits[source.Role] = max(roleLimits[source.Role], source.ByteLength)
		total += source.ByteLength
	}
	for _, failure := range []string{"boundary", "role+1", "aggregate+1", "declared+1", "count+1", "document+1"} {
		t.Run(failure, func(t *testing.T) {
			set, err := calibrationJSONCopy(original)
			require.NoError(t, err)
			bounds := qualificationBlobBounds{document: 1 << 20, count: len(set.Sources), total: total,
				role: func(role string) uint64 { return roleLimits[role] }}
			index := slices.IndexFunc(set.Sources, func(source qualificationSource) bool { return source.Role == "review" })
			switch failure {
			case "role+1":
				bytes := []byte(strings.Repeat("x", int(roleLimits["review"])+1))
				set.Sources[index].BytesBase64 = base64.StdEncoding.EncodeToString(bytes)
				set.Sources[index].ByteLength = uint64(len(bytes))
				set.Sources[index].SHA256 = byteSHA256(bytes)
				bounds.total++
			case "aggregate+1":
				bounds.total--
			case "declared+1":
				set.Sources[index].ByteLength++
			case "count+1":
				bounds.count--
			}
			// Canonical key order puts bytes_base64 before role and byte_length.
			data, err := qualificationSeal(set)
			require.NoError(t, err)
			switch failure {
			case "document+1":
				bounds.document = len(data.bytes()) - 1
			case "boundary":
				bounds.document = len(data.bytes())
			}
			reached := false
			_, err = qualificationParseSourcesBounded(data.bytes(), bounds, func() { reached = true })
			if failure == "boundary" {
				require.NoError(t, err)
				require.True(t, reached)
			} else {
				require.Error(t, err)
				require.False(t, reached, "tree/struct/string decoding must not begin")
			}
		})
	}
}

func TestQualificationScopedSourceCustodyFailsClosed(t *testing.T) {
	for _, failure := range []string{"missing authored", "duplicate scoped", "wrong path", "wrong arm",
		"extra scoped", "modified authored", "nonfinite authored", "wrong grader binding", "missing grader"} {
		t.Run(failure, func(t *testing.T) {
			fixture := qualificationSourceFixtureForTest(t)
			index := slices.IndexFunc(fixture.reads, func(read qualificationSourceRead) bool { return read.Role == "authored_input" })
			require.NotEqual(t, -1, index)
			read := fixture.reads[index]
			switch failure {
			case "missing authored":
				fixture.reads = slices.Delete(fixture.reads, index, index+1)
			case "duplicate scoped", "extra scoped":
				read.ID = "extra"
				if failure == "extra scoped" {
					copy := *read.Selector
					copy.CaseID = "extra-case"
					read.Selector = &copy
				}
				fixture.reads = append(fixture.reads, read)
			case "wrong path":
				data, err := os.ReadFile(filepath.Join(fixture.root, read.Path))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "other.json"), data, 0o600))
				fixture.reads[index].Path = "other.json"
			case "wrong arm":
				fixture.reads[index].Selector.ArmID = "other"
			case "modified authored", "nonfinite authored":
				data := []byte(`{"output":null}`)
				if failure == "nonfinite authored" {
					data = []byte(`{"output":1e9999}`)
				}
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, read.Path), data, 0o600))
			case "wrong grader binding", "missing grader":
				if failure == "wrong grader binding" {
					fixture.document.Cases[0].Checks[0].GraderDeclarationSHA256 = strings.Repeat("0", 64)
				} else {
					fixture.document.Cases[0].Checks[0].Check.Grader = "missing"
				}
				require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "labels.json"), marshalReferenceTest(t, fixture.document), 0o600))
			}
			sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
			require.Error(t, err)
			require.Empty(t, sources.native.canonical, "failed acquisition cannot yield an admitted handle")
		})
	}
}

func TestQualificationRubricInventoryUsesOriginalNativeBinding(t *testing.T) {
	for _, failure := range []string{"", "missing", "digest", "native path", "nonprompt", "null binding and absent source", "inline only", "pairwise", "continued session"} {
		t.Run("rubric-"+failure, func(t *testing.T) {
			fixture := qualificationSourceFixtureForTest(t)
			rubric := []byte("Original evaluator rubric — preserved verbatim\n")
			require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "rubric.md"), rubric, 0o600))
			if failure != "nonprompt" {
				fixture.request.Spec.Graders[0].Kind = models.GraderKindPrompt
				fixture.request.Spec.Graders[0].Parameters = models.PromptGraderParameters{
					Rubric: "rubric.md", Mode: models.PromptGraderModeIndependent,
				}
				parameters, ok := fixture.request.Spec.Graders[0].Parameters.(models.PromptGraderParameters)
				require.True(t, ok)
				switch failure {
				case "inline only":
					parameters.Rubric, parameters.Prompt = "", "inline-only"
				case "pairwise":
					parameters.Mode = models.PromptGraderModePairwise
				case "continued session":
					parameters.ContinueSession = true
				}
				fixture.request.Spec.Graders[0].Parameters = parameters
			}
			evalBytes, err := yaml.Marshal(fixture.request.Spec)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), evalBytes, 0o600))
			fixture.request.Spec, err = models.LoadEvalSpecOffline(filepath.Join(fixture.root, "eval.yaml"))
			require.NoError(t, err)
			fixture.document.EvalSourceSHA256 = byteSHA256(evalBytes)
			for i := range fixture.document.Cases {
				fixture.document.Cases[i].Checks[0].RubricContentSHA256 = new(byteSHA256(rubric))
				if failure == "null binding and absent source" {
					fixture.document.Cases[i].Checks[0].RubricContentSHA256 = nil
				}
				selector := *fixture.reads[5+i].Selector
				path := "rubric.md"
				if failure == "native path" {
					path = "other.md"
					require.NoError(t, os.WriteFile(filepath.Join(fixture.root, path), rubric, 0o600))
				}
				if failure != "missing" && failure != "null binding and absent source" {
					fixture.reads = append(fixture.reads, qualificationSourceRead{
						ID: "rubric-" + fixture.document.Cases[i].ID, Role: "rubric", TaskID: selector.TaskID,
						Selector: &selector, Root: fixture.request.SnapshotRoot, Path: path,
					})
				}
			}
			if failure == "digest" {
				fixture.document.Cases[0].Checks[0].RubricContentSHA256 = new(strings.Repeat("0", 64))
			}
			calibrationRebind(t, &fixture.request, &fixture.document)
			require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "labels.json"), marshalReferenceTest(t, fixture.document), 0o600))
			sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
			if failure == "" {
				require.NoError(t, err)
				retained, err := sources.source("rubric-" + fixture.document.Cases[0].ID)
				require.NoError(t, err)
				require.Equal(t, rubric, retained)
			} else {
				require.Error(t, err)
				require.Empty(t, sources.native.canonical)
			}
		})
	}
}

func TestQualificationNativeSelectionAggregateBudgetBeforeSerialization(t *testing.T) {
	fixture := qualificationSourceFixtureForTest(t)
	task, err := calibrationJSONCopy(fixture.request.Tasks["task"])
	require.NoError(t, err)
	task.TestID = "task-other"
	taskBytes, err := yaml.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "task-other.yaml"), taskBytes, 0o600))
	fixture.request.Spec.Tasks = append(fixture.request.Spec.Tasks, "task-other.yaml")
	evalBytes, err := yaml.Marshal(fixture.request.Spec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.root, "eval.yaml"), evalBytes, 0o600))
	fixture.reads = append(fixture.reads, qualificationSourceRead{
		ID: "task-other", Role: "task", TaskID: task.TestID, Root: fixture.request.SnapshotRoot, Path: "task-other.yaml",
	})
	sources, err := qualificationAcquireSources(t.Context(), fixture.reads)
	require.NoError(t, err)
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	require.NoError(t, err)
	native, err := qualificationDecode[qualificationNativeSelection](sources.native.bytes(), qualificationDocumentLimit)
	require.NoError(t, err)
	require.Len(t, native.Tasks, 2)
	reads := map[string]qualificationSourceRead{}
	for _, read := range fixture.reads {
		reads[read.ID] = read
	}
	limit := len(sources.native.bytes())
	require.Less(t, len(native.ResolvedSpec), limit-1)
	for _, task := range native.Tasks {
		require.Less(t, len(task.Declaration), limit-1)
	}
	reached := false
	serialized := []string{}
	document, err := qualificationLoadSelectionWithHooks(t.Context(), set, reads, limit,
		func() { reached = true }, func(id string) { serialized = append(serialized, id) })
	require.NoError(t, err)
	require.True(t, reached)
	require.Equal(t, []string{"spec", "task", "task-other"}, serialized)
	require.Equal(t, sources.native.bytes(), document.bytes())
	reached = false
	serialized = nil
	document, err = qualificationLoadSelectionWithHooks(t.Context(), set, reads, limit-1,
		func() { reached = true }, func(id string) { serialized = append(serialized, id) })
	require.Error(t, err)
	require.False(t, reached, "individually bounded tasks cannot bypass aggregate native projection budget")
	require.Equal(t, []string{"spec", "task"}, serialized, "over-budget task must not be serialized or retained")
	require.Empty(t, document.canonical)
}

func TestQualificationSnapshotYAMLRejectsAmbiguityAndExternalResolution(t *testing.T) {
	for _, data := range []string{
		"x: 1\nx: 2\n", "x: &a {}\ny: *a\n", "x: 1\n---\nx: 2\n",
		"x: { $ref: 'https://example.invalid/schema' }\n", "x: { schema_file: 'other.json' }\n", "\x00",
		"{", "x: { ref: other }\n",
	} {
		require.Error(t, qualificationNoIncludes([]byte(data)), data)
	}
	require.NoError(t, qualificationNoIncludes([]byte("x: é\n")))
	deep := strings.Repeat("[", 70) + "0" + strings.Repeat("]", 70)
	require.Error(t, qualificationNoIncludes([]byte(deep)))
	var set qualificationSourceSet
	require.NoError(t, json.Unmarshal([]byte(`{"sources":null}`), &set))
	_, err := qualificationParseSources(marshalReferenceTest(t, set))
	require.Error(t, err)
}
