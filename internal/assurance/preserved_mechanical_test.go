package assurance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/snapshot"
)

// These complete native declarations are synthetic finite controls. Neither
// their origin nor runtime is a claim of historical/provider authenticity.
type preservedFixture struct {
	t        *testing.T
	input    PreservedMechanicalInput
	root     map[string]any
	manifest *models.EvidenceManifest
}

func preservedTestValue[T any](t *testing.T, value any) T {
	t.Helper()
	typed, ok := value.(T)
	require.True(t, ok, "unexpected synthetic fixture type %T", value)
	return typed
}

func (f *preservedFixture) array(key string) []any {
	f.t.Helper()
	return preservedTestValue[[]any](f.t, f.root[key])
}

func (f *preservedFixture) object(key string) map[string]any {
	f.t.Helper()
	return preservedTestValue[map[string]any](f.t, f.root[key])
}

func (f *preservedFixture) event(index int) map[string]any {
	f.t.Helper()
	return preservedTestValue[map[string]any](f.t, f.array("toolEvents")[index])
}

func (f *preservedFixture) file(index int) map[string]any {
	f.t.Helper()
	return preservedTestValue[map[string]any](f.t, f.array("workspaceFiles")[index])
}

type preservedCancelAfterGrade struct {
	context.Context
	armed bool
	calls int
}

func (ctx *preservedCancelAfterGrade) Err() error {
	if ctx.armed {
		ctx.calls++
		if ctx.calls > 1 {
			return context.Canceled
		}
	}
	return ctx.Context.Err()
}

func preservedEvent(name string, sequence int, args any) map[string]any {
	event := map[string]any{
		"tool_name": name, "sequence": sequence, "turn": 0, "success": true,
		"tool_call_id": fmt.Sprintf("finite-%d", sequence),
	}
	if args != nil {
		event["args"] = args
	}
	return event
}

func preservedFile(path, content string) snapshot.WorkspaceFile {
	sum := sha256.Sum256([]byte(content))
	return snapshot.WorkspaceFile{Path: path, Content: content, SHA256: hex.EncodeToString(sum[:])}
}

func newPreservedFixture(t *testing.T, kind models.GraderKind, params models.GraderParameters, tape []any, files []snapshot.WorkspaceFile) *preservedFixture {
	t.Helper()
	task := &models.TestCase{TestID: "synthetic-finite-task"}
	config := models.GraderConfig{Identifier: "check", Kind: kind, Parameters: params}
	origin := models.EvidenceOrigin{
		EvalID: "synthetic-finite-native", TaskID: task.TestID, RunNumber: 1, AttemptCount: 1, PriorAttempts: "none",
	}
	f := &preservedFixture{
		t: t,
		input: PreservedMechanicalInput{
			Task: task, Spec: &models.EvalSpec{Graders: []models.GraderConfig{config}},
			Check: models.RequirementCheck{Scope: "eval", Grader: "check"},
		},
		root: map[string]any{
			"schemaVersion": snapshot.CurrentSchemaVersion, "kind": snapshot.Kind,
			"createdAt": "2026-10-09T00:00:00Z", "evalId": origin.EvalID,
			"task":       map[string]any{"testId": task.TestID, "runNumber": 1},
			"toolEvents": tape, "nativeDeclaration": config,
		},
		manifest: &models.EvidenceManifest{
			Version: evidence.Version, Origin: origin,
			Runtime: models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
		},
	}
	add := func(id, kind, pointer string) {
		f.manifest.Artifacts = append(f.manifest.Artifacts, models.EvidenceArtifact{
			ID: id, Kind: kind, Document: "snapshot", Pointer: pointer,
			Availability: "captured", Completeness: "complete",
		})
		f.input.Evidence = append(f.input.Evidence, models.EvidenceReference{Origin: origin, ArtifactID: id})
	}
	add("tool-events", "tool_events", "/toolEvents")
	add("native-declaration", "synthetic_fixture_declaration", "/nativeDeclaration")
	rawFiles := make([]any, 0, len(files))
	for index, file := range files {
		rawFiles = append(rawFiles, map[string]any{
			"path": file.Path, "content": file.Content, "sha256": file.SHA256, "redacted": file.Redacted,
		})
		add("workspace-file/"+file.Path, "workspace_file", fmt.Sprintf("/workspaceFiles/%d", index))
	}
	f.root["workspaceFiles"] = rawFiles
	f.seal(t)
	return f
}

func (f *preservedFixture) seal(t *testing.T) {
	t.Helper()
	for index := range f.manifest.Artifacts {
		artifact := &f.manifest.Artifacts[index]
		if artifact.Availability != "captured" {
			continue
		}
		value, err := pointerValue(f.root, artifact.Pointer)
		if err != nil {
			continue
		}
		artifact.ContentDigest, err = evidence.JSONDigest(value)
		require.NoError(t, err)
	}
	require.NoError(t, evidence.Seal(f.manifest))
	f.input.ManifestSHA256 = f.manifest.SHA256
	f.encode(t)
}

func (f *preservedFixture) encode(t *testing.T) {
	t.Helper()
	f.root["evidence"] = f.manifest
	var err error
	f.input.SnapshotBytes, err = json.Marshal(f.root)
	require.NoError(t, err)
}

func requirePreservedResult(t *testing.T, observation Observation, passed bool, score float64, feedback string) {
	t.Helper()
	require.Equal(t, Observed, observation.State, "%v", observation.Err)
	require.NoError(t, observation.Err)
	require.NotNil(t, observation.Result)
	require.Equal(t, passed, observation.Result.Passed)
	require.InDelta(t, score, observation.Result.Score, 1e-12)
	require.Contains(t, observation.Result.Feedback, feedback)
}

func TestPreservedMechanicalNativeFinite12By4(t *testing.T) {
	// Twelve domain/configuration pairs share the exact declaration across four
	// candidates. State and forbidden-side-effect verdicts are separate native
	// observations; this corpus is not a reviewed-label assurance report.
	count := 0
	for _, domain := range []string{"mcp", "cli", "repo"} {
		for _, family := range []string{"tool_calls", "tool_constraint", "sequence", "allow_only"} {
			t.Run(domain+"/"+family, func(t *testing.T) {
				write, destroy := domain+"_write", domain+"_destroy"
				matcher := argmatcher.Matcher{Kind: argmatcher.KindRegex, Regex: `^src/(primary|alternative)\.txt$`}
				var params models.GraderParameters
				var kind models.GraderKind
				switch family {
				case "tool_calls":
					kind = models.GraderKindToolCalls
					params = models.ToolCallsGraderParameters{ForbiddenTools: []string{destroy},
						Expect: []models.ToolExpectation{{Tool: "^" + write + "$", Args: map[string]argmatcher.Matcher{"path": matcher}}}}
				case "tool_constraint":
					kind = models.GraderKindToolConstraint
					params = models.ToolConstraintGraderParameters{
						ExpectTools: []models.ToolSpecParameters{{Tool: "^" + write + "$", PathPattern: `^src/(primary|alternative)\.txt$`}},
						RejectTools: []models.ToolSpecParameters{{Tool: "^" + destroy + "$"}},
					}
				case "sequence":
					kind = models.GraderKindActionSequence
					params = models.ActionSequenceGraderParameters{
						ExpectedActions: []string{write, write}, MatchingMode: models.ActionSequenceMatchingModeExact,
					}
				case "allow_only":
					kind = models.GraderKindToolConstraint
					params = models.ToolConstraintGraderParameters{
						AllowOnly: &[]models.ToolSpecParameters{{Tool: write, Args: map[string]argmatcher.Matcher{"path": matcher}}},
					}
				}
				declared, err := evidence.JSONDigest(params)
				require.NoError(t, err)
				for _, candidate := range []string{"good", "alternative-valid", "critical-wrong-state", "forbidden-action"} {
					t.Run(candidate, func(t *testing.T) {
						count++
						path, content := "src/primary.txt", "state: ready\n"
						if candidate == "alternative-valid" {
							path, content = "src/alternative.txt", "# alternate route\nstate: ready\n"
						}
						if candidate == "critical-wrong-state" {
							content = "state: wrong\n"
						}
						tape := []any{preservedEvent(write, 1, map[string]any{"path": path})}
						if family == "sequence" {
							tape = append(tape, preservedEvent(write, 2, map[string]any{"path": path}))
						}
						if candidate == "forbidden-action" {
							tape = append(tape, preservedEvent(destroy, len(tape)+1, nil))
						}
						f := newPreservedFixture(t, kind, params, tape, []snapshot.WorkspaceFile{preservedFile("state.txt", content)})
						before := bytes.Clone(f.input.SnapshotBytes)
						result := ObservePreservedMechanical(t.Context(), f.input)
						require.Equal(t, before, f.input.SnapshotBytes)
						pass, score, feedback := true, 1.0, "passed"
						if family == "sequence" {
							feedback = "Action sequence matched"
						}
						if candidate == "forbidden-action" {
							pass, score = false, 0.5
							switch family {
							case "tool_calls":
								feedback = "forbidden tool"
							case "tool_constraint":
								feedback = "Rejected tool was used"
							case "sequence":
								score, feedback = 0.8, "Exact match failed"
							case "allow_only":
								feedback = "Undeclared tool used"
							}
						}
						requirePreservedResult(t, result, pass, score, feedback)
						fileParams := models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
							Path: "state.txt", MustMatch: []string{`(?m)^state: ready$`},
						}}}
						f.input.Spec = &models.EvalSpec{Graders: []models.GraderConfig{{Identifier: "check", Kind: models.GraderKindFile, Parameters: fileParams}}}
						f.root["nativeDeclaration"] = f.input.Spec.Graders[0]
						f.seal(t)
						filePass, fileScore, fileFeedback := true, 1.0, "passed"
						if candidate == "critical-wrong-state" {
							filePass, fileScore, fileFeedback = false, 0.5, "missing expected pattern"
						}
						requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), filePass, fileScore, fileFeedback)
						after, err := evidence.JSONDigest(params)
						require.NoError(t, err)
						require.Equal(t, declared.SHA256, after.SHA256)
					})
				}
			})
		}
	}
	require.Equal(t, 48, count)
}

func TestPreservedMechanicalRawTapeAdmission(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		mutate func(*preservedFixture)
		state  ObservationState
	}{
		{"absent tape", func(f *preservedFixture) { delete(f.root, "toolEvents") }, InsufficientEvidence},
		{"null tape", func(f *preservedFixture) { f.root["toolEvents"] = nil }, InsufficientEvidence},
		{"object tape", func(f *preservedFixture) { f.root["toolEvents"] = map[string]any{} }, Invalid},
		{"unknown capture", func(f *preservedFixture) {
			f.manifest.Artifacts[0].Completeness, f.manifest.Artifacts[0].Reason = "unknown", "Collector completeness is not certified"
		}, InsufficientEvidence},
		{"partial capture", func(f *preservedFixture) {
			f.manifest.Artifacts[0].Completeness, f.manifest.Artifacts[0].Reason = "partial", "Execution failed"
		}, InsufficientEvidence},
		{"redacted", func(f *preservedFixture) { f.manifest.Artifacts[0].Redacted = true }, InsufficientEvidence},
		{"child selection only", func(f *preservedFixture) { f.input.Evidence[0].Pointer = "/0/tool_name" }, InsufficientEvidence},
		{"invalid child pointer", func(f *preservedFixture) { f.input.Evidence[0].Pointer = "/~2" }, Invalid},
		{"absent child pointer", func(f *preservedFixture) { f.input.Evidence[0].Pointer = "/0/absent" }, Invalid},
		{"wrong locator", func(f *preservedFixture) { f.manifest.Artifacts[0].Pointer = "/workspaceFiles" }, Invalid},
		{"wrong kind", func(f *preservedFixture) { f.manifest.Artifacts[0].Kind = "workspace_file" }, Invalid},
		{"wrong origin", func(f *preservedFixture) { f.input.Evidence[0].Origin.RunNumber++ }, Invalid},
		{"wrong task", func(f *preservedFixture) { f.input.Task.TestID = "other" }, Invalid},
		{"wrong snapshot run", func(f *preservedFixture) { f.object("task")["runNumber"] = 2 }, Invalid},
		{"wrong eval", func(f *preservedFixture) { f.root["evalId"] = "other" }, Invalid},
		{"missing kind", func(f *preservedFixture) { delete(f.root, "kind") }, Invalid},
		{"missing version", func(f *preservedFixture) { delete(f.root, "schemaVersion") }, Invalid},
		{"missing manifest", func(f *preservedFixture) { f.manifest = nil }, InsufficientEvidence},
		{"duplicate ID", func(f *preservedFixture) {
			f.root["toolEvents"] = append(f.array("toolEvents"), preservedEvent("other", 2, nil))
			f.event(1)["tool_call_id"] = "finite-1"
		}, Invalid},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{RequiredTools: []string{"write"}}, []any{preservedEvent("write", 1, nil)}, nil)
			candidate.mutate(f)
			if f.manifest != nil {
				f.seal(t)
			} else {
				f.encode(t)
			}
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, candidate.state, result.State, "%v", result.Err)
			require.Error(t, result.Err)
			require.Nil(t, result.Result)
		})
	}
	for _, field := range []string{"tool_name", "sequence", "turn", "success", "tool_call_id", "duration_ms"} {
		for _, value := range []any{nil, "wrong", -1, 1.5, map[string]any{}} {
			t.Run(fmt.Sprintf("%s/%v", field, value), func(t *testing.T) {
				f := newPreservedFixture(t, models.GraderKindToolCalls,
					models.ToolCallsGraderParameters{RequiredTools: []string{"write"}}, []any{preservedEvent("write", 1, nil)}, nil)
				f.event(0)[field] = value
				f.seal(t)
				result := ObservePreservedMechanical(t.Context(), f.input)
				if field == "tool_name" && value == "wrong" || field == "tool_call_id" && value == "wrong" {
					require.Equal(t, Observed, result.State, "%v", result.Err)
					return
				}
				require.NotEqual(t, Observed, result.State)
				require.Nil(t, result.Result)
			})
		}
	}
	for _, field := range []string{"tool_name", "sequence", "turn", "success"} {
		t.Run("missing/"+field, func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{RequiredTools: []string{"write"}}, []any{preservedEvent("write", 1, nil)}, nil)
			delete(f.event(0), field)
			f.seal(t)
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, InsufficientEvidence, result.State)
			require.Nil(t, result.Result)
		})
	}
	for _, runtime := range []string{"mock", "live", "unknown"} {
		t.Run("runtime-does-not-certify/"+runtime, func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MaxCalls: new(0)}, []any{}, nil)
			f.manifest.Runtime.ExecutionMode = runtime
			f.seal(t)
			requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
			f.manifest.Artifacts[0].Completeness, f.manifest.Artifacts[0].Reason = "unknown", "Collector completeness is not certified"
			f.seal(t)
			require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
		})
	}
}

func TestPreservedMechanicalArgumentBoundariesAndNativeSemantics(t *testing.T) {
	params := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{
		Tool: "^write$", Args: map[string]argmatcher.Matcher{"path": {Kind: argmatcher.KindEquals, Equals: "src/a"}},
	}}}
	for _, candidate := range []struct {
		name  string
		args  any
		state ObservationState
	}{
		{"absent", nil, InsufficientEvidence},
		{"null", json.RawMessage("null"), InsufficientEvidence},
		{"empty explicit object", map[string]any{}, Observed},
		{"nonobject", []any{}, OperationalError},
		{"no numeric coercion", map[string]any{"path": 1}, OperationalError},
		{"no null coercion", map[string]any{"path": nil}, OperationalError},
		{"valid string", map[string]any{"path": "src/a"}, Observed},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindToolCalls, params, []any{preservedEvent("write", 1, candidate.args)}, nil)
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, candidate.state, result.State, "%v", result.Err)
			if candidate.state == Observed {
				require.Equal(t, candidate.name == "valid string", result.Result.Passed)
			} else {
				require.Nil(t, result.Result)
			}
		})
	}
	t.Run("irrelevant nonobject retained", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, params, []any{
			preservedEvent("write", 1, map[string]any{"path": "src/a"}), preservedEvent("other", 2, []any{"not-consumed"}),
		}, nil)
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
	})
	t.Run("canonical empty string differs from legacy digest", func(t *testing.T) {
		matchers := map[string]argmatcher.Matcher{"path": {Kind: argmatcher.KindEquals, Equals: ""}}
		f := newPreservedFixture(t, models.GraderKindToolCalls,
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "^write$", Args: matchers}}},
			[]any{preservedEvent("write", 1, map[string]any{"path": ""})}, nil)
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
		f.input.Spec.Graders[0].Parameters = models.ToolConstraintGraderParameters{
			ExpectTools: []models.ToolSpecParameters{{Tool: "^write$", Args: matchers}},
		}
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), false, 0, "Expected tool not used")
	})
	t.Run("MCP nested Extra plus known fields", func(t *testing.T) {
		matchers := map[string]argmatcher.Matcher{
			"query": {Kind: argmatcher.KindEquals, Equals: map[string]any{"state": "ready"}},
			"path":  {Kind: argmatcher.KindEquals, Equals: "src/a"},
		}
		tape := []any{preservedEvent("mcp.write", 1, map[string]any{
			"path": "src/a", "query": map[string]any{"state": "ready"}, "limit": 3,
		})}
		for _, params := range []models.GraderParameters{
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: `^mcp\.write$`, Args: matchers}}},
			models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: `^mcp\.write$`, Args: matchers}}},
		} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, params, tape, nil)
			requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
		}
	})
	t.Run("constraint patterns consume arguments", func(t *testing.T) {
		for _, spec := range []models.ToolSpecParameters{
			{Tool: "^write$", PathPattern: "src"}, {Tool: "^write$", CommandPattern: "safe"}, {Tool: "^write$", SkillPattern: "safe"},
		} {
			for _, params := range []models.GraderParameters{
				models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{spec}},
				models.ToolConstraintGraderParameters{RejectTools: []models.ToolSpecParameters{spec}},
				models.ToolConstraintGraderParameters{AllowOnly: &[]models.ToolSpecParameters{{Tool: "write", PathPattern: spec.PathPattern, CommandPattern: spec.CommandPattern, SkillPattern: spec.SkillPattern}}},
			} {
				f := newPreservedFixture(t, models.GraderKindToolConstraint, params, []any{preservedEvent("write", 1, nil)}, nil)
				require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
			}
		}
	})
}

func TestPreservedMechanicalProjectionDetachmentAndJoins(t *testing.T) {
	params := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{
		Tool: "^write$", Args: map[string]argmatcher.Matcher{"query": {Kind: argmatcher.KindEquals, Equals: "ready"}},
	}}}
	for _, ids := range []bool{true, false} {
		f := newPreservedFixture(t, models.GraderKindToolCalls, params, []any{
			preservedEvent("write", 1, map[string]any{"query": "wrong"}),
			preservedEvent("write", 2, map[string]any{"query": "ready"}),
		}, nil)
		if !ids {
			for index := range f.array("toolEvents") {
				delete(f.event(index), "tool_call_id")
			}
		}
		f.seal(t)
		before := bytes.Clone(f.input.SnapshotBytes)
		result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(gCtx *graders.Context) {
			require.Equal(t, []string{"write", "write"}, gCtx.Session.ToolsUsed)
			require.Equal(t, 2, gCtx.Session.ToolCallCount)
			require.Nil(t, gCtx.Session.Usage)
			require.Nil(t, gCtx.Session.Errors)
			require.Empty(t, gCtx.Session.SessionID)
			require.Nil(t, gCtx.Executor)
			require.Nil(t, gCtx.SkillInvocations)
			require.Zero(t, gCtx.ToolEvents[0].Turn)
			// Source mutations after preparation cannot change selected config,
			// raw bytes, canonical events, or projected argument maps.
			f.input.Spec.Graders[0].Parameters = models.ProgramGraderParameters{Command: "never-run"}
			f.event(1)["args"] = nil
			clear(f.input.SnapshotBytes)
			gCtx.Session.ToolCalls[1].Arguments.Extra["query"] = "wrong"
			require.Equal(t, "ready", preservedTestValue[map[string]any](t, gCtx.ToolEvents[1].Args)["query"])
		}})
		requirePreservedResult(t, result, true, 1, "passed")
		expectations := preservedTestValue[[]map[string]any](t, result.Result.Details["expect"])
		require.Equal(t, 1, expectations[0]["matched_call_index"])
		require.NotEqual(t, before, f.input.SnapshotBytes)
	}
}

func TestPreservedMechanicalFileAdmissionAndIsolation(t *testing.T) {
	params := models.FileGraderParameters{MustExist: []string{"state.txt"}, ContentPatterns: []models.FileContentPatternParameters{{
		Path: "state.txt", MustMatch: []string{"ready"}, MustNotMatch: []string{"forbidden"},
	}}}
	for _, candidate := range []struct {
		name   string
		mutate func(*preservedFixture)
		state  ObservationState
	}{
		{"missing selection", func(f *preservedFixture) { f.input.Evidence = f.input.Evidence[:2] }, InsufficientEvidence},
		{"child selection", func(f *preservedFixture) { f.input.Evidence[2].Pointer = "/content" }, InsufficientEvidence},
		{"null files", func(f *preservedFixture) { f.root["workspaceFiles"] = nil }, InsufficientEvidence},
		{"null content", func(f *preservedFixture) { f.file(0)["content"] = nil }, InsufficientEvidence},
		{"absent content", func(f *preservedFixture) { delete(f.file(0), "content") }, InsufficientEvidence},
		{"redacted file", func(f *preservedFixture) { f.file(0)["redacted"] = true }, InsufficientEvidence},
		{"wrong file SHA", func(f *preservedFixture) {
			f.file(0)["sha256"] = strings.Repeat("0", 64)
		}, Invalid},
		{"wrong kind", func(f *preservedFixture) { f.manifest.Artifacts[2].Kind = "tool_events" }, Invalid},
		{"wrong file locator", func(f *preservedFixture) { f.manifest.Artifacts[2].Pointer = "/nativeDeclaration" }, Invalid},
		{"duplicate file", func(f *preservedFixture) {
			f.root["workspaceFiles"] = append(f.array("workspaceFiles"), f.file(0))
		}, Invalid},
		{"escaping declaration", func(f *preservedFixture) {
			f.input.Spec.Graders[0].Parameters = models.FileGraderParameters{MustExist: []string{"../state.txt"}}
		}, Invalid},
		{"absence not inferable", func(f *preservedFixture) {
			f.input.Spec.Graders[0].Parameters = models.FileGraderParameters{MustNotExist: []string{"forbidden.txt"}}
		}, InsufficientEvidence},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindFile, params, []any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", "ready")})
			candidate.mutate(f)
			f.seal(t)
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, candidate.state, result.State, "%v", result.Err)
			require.Nil(t, result.Result)
		})
	}
	for _, content := range []string{"", "ready forbidden", "ready"} {
		f := newPreservedFixture(t, models.GraderKindFile, params, []any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", content)})
		before := bytes.Clone(f.input.SnapshotBytes)
		score, feedback := 0.75, "missing expected pattern"
		if content == "ready forbidden" {
			feedback = "contains forbidden pattern"
		}
		if content == "ready" {
			score, feedback = 1, "passed"
		}
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), content == "ready", score, feedback)
		require.Equal(t, before, f.input.SnapshotBytes)
	}
	t.Run("required files only and private state", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile, params, []any{}, []snapshot.WorkspaceFile{
			preservedFile("state.txt", "ready"), preservedFile("evaluator-only.txt", "never materialize"),
		})
		var directory string
		result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(gCtx *graders.Context) {
			directory = gCtx.WorkspaceDir
			content, err := os.ReadFile(filepath.Join(directory, "state.txt"))
			require.NoError(t, err)
			require.Equal(t, "ready", string(content))
			_, err = os.Stat(filepath.Join(directory, "evaluator-only.txt"))
			require.ErrorIs(t, err, os.ErrNotExist)
			f.file(0)["content"] = "forbidden"
		}})
		requirePreservedResult(t, result, true, 1, "passed")
		_, err := os.Stat(directory)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestPreservedMechanicalErrorsCancellationAndLegacy(t *testing.T) {
	t.Run("raw malformed JSON and native key aliases", func(t *testing.T) {
		for _, data := range []string{`[]`, `null`, `{`, `{"kind":"task-snapshot","kind":"other"}`} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{}, nil)
			f.input.SnapshotBytes = []byte(data)
			require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		}
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{}, nil)
		f.root["ToolEvents"] = []any{}
		f.encode(t)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("manifest and raw digest tampering", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{}, nil)
		f.input.ManifestSHA256 = strings.Repeat("0", 64)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		f.seal(t)
		f.root["toolEvents"] = []any{preservedEvent("unexpected", 1, nil)}
		f.encode(t)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		f.seal(t)
		f.manifest.Origin.AttemptCount++
		f.encode(t)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("unsupported does not run", func(t *testing.T) {
		for _, params := range []models.GraderParameters{
			models.ProgramGraderParameters{Command: "never-run"}, models.PromptGraderParameters{Prompt: "never-run"},
			models.InlineScriptGraderParameters{}, models.BehaviorGraderParameters{MaxToolCalls: 1},
			models.SkillInvocationGraderParameters{}, models.DiffGraderParameters{},
		} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, params, []any{}, nil)
			require.Equal(t, NotAssessed, ObservePreservedMechanical(t.Context(), f.input).State)
		}
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{}, nil)
		f.input.Check.Scope, f.input.Check.AfterTurn = "checkpoint", 1
		require.Equal(t, NotAssessed, ObservePreservedMechanical(t.Context(), f.input).State)
		f.input.Check.Scope, f.input.Check.AfterTurn = "eval", 0
		f.input.Check.Grader = "missing"
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("cancellation and cleanup preserve error boundaries", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"state.txt"}},
			[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", "ready")})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result := ObservePreservedMechanical(ctx, f.input)
		require.Equal(t, OperationalError, result.State)
		require.ErrorIs(t, result.Err, context.Canceled)
		require.Nil(t, result.Result)
		ctx, cancel = context.WithCancel(t.Context())
		result = observePreservedMechanical(ctx, f.input, preservedMechanicalHooks{prepared: func(*graders.Context) { cancel() }})
		require.Equal(t, OperationalError, result.State)
		require.ErrorIs(t, result.Err, context.Canceled)
		operational := errors.New("synthetic cleanup error")
		result = observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{close: func(close func() error) error {
			return errors.Join(close(), operational)
		}})
		require.Equal(t, OperationalError, result.State)
		require.ErrorIs(t, result.Err, operational)
		require.True(t, result.Result.Passed)
		ctxAfterGrade := &preservedCancelAfterGrade{Context: t.Context()}
		result = observePreservedMechanical(ctxAfterGrade, f.input, preservedMechanicalHooks{
			prepared: func(*graders.Context) { ctxAfterGrade.armed = true },
			close:    func(close func() error) error { return errors.Join(close(), operational) },
		})
		require.Equal(t, OperationalError, result.State)
		require.ErrorIs(t, result.Err, context.Canceled)
		require.ErrorIs(t, result.Err, operational)
		require.True(t, result.Result.Passed)
	})
	t.Run("native task selection and unchanged legacy Grade", func(t *testing.T) {
		params := models.ToolCallsGraderParameters{RequiredTools: []string{"write"}}
		f := newPreservedFixture(t, models.GraderKindToolCalls, params, []any{preservedEvent("write", 1, nil)}, nil)
		f.input.Task.Validators = []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindToolCalls, Parameters: params}}
		f.input.Check.Scope = "task"
		result := ObservePreservedMechanical(t.Context(), f.input)
		requirePreservedResult(t, result, true, 1, "passed")
		legacy := ObserveDeclaredMechanical(t.Context(), f.input.Task, f.input.Spec, f.input.Check, &graders.Context{
			Session: &models.SessionDigest{ToolCalls: []models.ToolCall{{Name: "write"}}},
		})
		require.Equal(t, legacy.Result.Passed, result.Result.Passed)
		require.Equal(t, legacy.Result.Score, result.Result.Score)
		require.Equal(t, legacy.Result.Feedback, result.Result.Feedback)
	})
}

func TestPreservedMechanicalAdditionalNativeBoundaries(t *testing.T) {
	for _, mode := range []models.ActionSequenceMatchingMode{
		models.ActionSequenceMatchingModeExact, models.ActionSequenceMatchingModeInOrder, models.ActionSequenceMatchingModeAnyOrder,
	} {
		for _, candidate := range []struct {
			names []string
			pass  bool
		}{
			{[]string{"read", "write", "write"}, true},
			{[]string{"write", "read", "write"}, mode == models.ActionSequenceMatchingModeAnyOrder},
			{[]string{"read", "other", "write", "write"}, mode != models.ActionSequenceMatchingModeExact},
			{[]string{"read", "write"}, false},
		} {
			t.Run(string(mode)+"/"+strings.Join(candidate.names, ","), func(t *testing.T) {
				tape := make([]any, 0, len(candidate.names))
				for index, name := range candidate.names {
					tape = append(tape, preservedEvent(name, index+1, nil))
				}
				f := newPreservedFixture(t, models.GraderKindActionSequence, models.ActionSequenceGraderParameters{
					ExpectedActions: []string{"read", "write", "write"}, MatchingMode: mode,
				}, tape, nil)
				result := ObservePreservedMechanical(t.Context(), f.input)
				require.Equal(t, Observed, result.State, "%v", result.Err)
				require.Equal(t, candidate.pass, result.Result.Passed)
			})
		}
	}
}

func TestPreservedMechanicalAdapterOwnedSizeCaps(t *testing.T) {
	require.Equal(t, 16*1024*1024, maxSnapshotBytes)
	require.Equal(t, 4*1024*1024, preservedMaxFileBytes)
	require.Equal(t, 8*1024*1024, preservedMaxSelectedBytes)
	require.Equal(t, 256, preservedMaxSelectedFiles)
	for _, size := range []int{maxSnapshotBytes, maxSnapshotBytes + 1} {
		t.Run(fmt.Sprintf("raw snapshot bytes/%d", size), func(t *testing.T) {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{MaxCalls: new(0)}, []any{}, nil)
			f.input.SnapshotBytes = append(bytes.Clone(f.input.SnapshotBytes), bytes.Repeat([]byte(" "), size-len(f.input.SnapshotBytes))...)
			require.Len(t, f.input.SnapshotBytes, size)
			prepared := false
			result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) { prepared = true }})
			if size == maxSnapshotBytes {
				requirePreservedResult(t, result, true, 1, "passed")
				require.True(t, prepared)
			} else {
				require.Equal(t, Invalid, result.State)
				require.ErrorContains(t, result.Err, "raw snapshot")
				require.Nil(t, result.Result)
				require.False(t, prepared)
			}
		})
	}
	for _, size := range []int{preservedMaxFileBytes, preservedMaxFileBytes + 1} {
		t.Run(fmt.Sprintf("decoded per-file bytes/%d", size), func(t *testing.T) {
			// Byte limits apply to decoded UTF-8, not character count or the
			// enclosing JSON artifact's size.
			content := strings.Repeat("é", preservedMaxFileBytes/2)
			if size > preservedMaxFileBytes {
				content += "x"
			}
			require.Len(t, content, size)
			f := newPreservedFixture(t, models.GraderKindFile,
				models.FileGraderParameters{MustExist: []string{"state.txt"}},
				[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", content)})
			prepared := false
			result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) { prepared = true }})
			if size == preservedMaxFileBytes {
				requirePreservedResult(t, result, true, 1, "passed")
				require.True(t, prepared)
			} else {
				require.Equal(t, Invalid, result.State)
				require.ErrorContains(t, result.Err, "decoded bytes")
				require.Nil(t, result.Result)
				require.False(t, prepared)
			}
		})
	}
	for _, size := range []int{preservedMaxSelectedBytes, preservedMaxSelectedBytes + 1} {
		t.Run(fmt.Sprintf("decoded selected aggregate bytes/%d", size), func(t *testing.T) {
			content := strings.Repeat("r", preservedMaxFileBytes)
			files := []snapshot.WorkspaceFile{preservedFile("one.txt", content), preservedFile("two.txt", content)}
			paths := []string{"one.txt", "two.txt"}
			if size > preservedMaxSelectedBytes {
				files = append(files, preservedFile("three.txt", "x"))
				paths = append(paths, "three.txt")
			}
			f := newPreservedFixture(t, models.GraderKindFile,
				models.FileGraderParameters{MustExist: paths}, []any{}, files)
			prepared := false
			result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) { prepared = true }})
			if size == preservedMaxSelectedBytes {
				requirePreservedResult(t, result, true, 1, "passed")
				require.True(t, prepared)
			} else {
				require.Equal(t, Invalid, result.State)
				require.ErrorContains(t, result.Err, "adapter caps")
				require.Nil(t, result.Result)
				require.False(t, prepared)
			}
		})
	}
	for _, count := range []int{preservedMaxSelectedFiles, preservedMaxSelectedFiles + 1} {
		t.Run(fmt.Sprintf("selected count/%d", count), func(t *testing.T) {
			files := make([]snapshot.WorkspaceFile, 0, count)
			paths := make([]string, 0, count)
			for index := range count {
				path := fmt.Sprintf("finite/%03d.txt", index)
				paths = append(paths, path)
				files = append(files, preservedFile(path, ""))
			}
			f := newPreservedFixture(t, models.GraderKindFile,
				models.FileGraderParameters{MustExist: paths}, []any{}, files)
			prepared := false
			result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) { prepared = true }})
			if count == preservedMaxSelectedFiles {
				requirePreservedResult(t, result, true, 1, "passed")
				require.True(t, prepared)
			} else {
				require.Equal(t, Invalid, result.State)
				require.ErrorContains(t, result.Err, "adapter caps")
				require.Nil(t, result.Result)
				require.False(t, prepared)
			}
		})
	}
	t.Run("caps cover additional selected files without materializing them", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile,
			models.FileGraderParameters{MustExist: []string{"required.txt"}}, []any{},
			[]snapshot.WorkspaceFile{
				preservedFile("required.txt", "ready"),
				preservedFile("additional-selected.txt", strings.Repeat("r", preservedMaxFileBytes+1)),
			})
		result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) {
			t.Fatal("oversized selected file must be rejected before materialization and grading")
		}})
		require.Equal(t, Invalid, result.State)
		require.Nil(t, result.Result)
	})
	t.Run("duplicate selected references do not multiply decoded bytes", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile,
			models.FileGraderParameters{MustExist: []string{"state.txt"}}, []any{},
			[]snapshot.WorkspaceFile{preservedFile("state.txt", strings.Repeat("r", preservedMaxFileBytes))})
		for range 3 {
			f.input.Evidence = append(f.input.Evidence, f.input.Evidence[2])
		}
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
	})
}

func TestPreservedMechanicalNativeEmptyStringNormalizationCompatibility(t *testing.T) {
	for _, key := range []string{"path", "file_text", "command", "description", "skill", "mcp_extra"} {
		t.Run(key, func(t *testing.T) {
			matchers := map[string]argmatcher.Matcher{key: {Kind: argmatcher.KindEquals, Equals: ""}}
			for _, candidate := range []struct {
				name   string
				params models.GraderParameters
				pass   bool
			}{
				{"canonical tool_calls", models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "^write$", Args: matchers}}}, true},
				{"legacy digest tool_constraint", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "^write$", Args: matchers}}}, key == "mcp_extra"},
			} {
				t.Run(candidate.name, func(t *testing.T) {
					f := newPreservedFixture(t, models.GraderKindToolCalls, candidate.params,
						[]any{preservedEvent("write", 1, map[string]any{key: ""})}, nil)
					feedback, score := "passed", 1.0
					if !candidate.pass {
						feedback, score = "Expected tool not used", 0
					}
					requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), candidate.pass, score, feedback)
					f.event(0)["args"] = map[string]any{}
					f.seal(t)
					result := ObservePreservedMechanical(t.Context(), f.input)
					require.Equal(t, Observed, result.State, "%v", result.Err)
					require.False(t, result.Result.Passed)
					require.Zero(t, result.Result.Score)
				})
			}
		})
	}
	for _, spec := range []models.ToolSpecParameters{
		{Tool: "^write$", PathPattern: "^$"}, {Tool: "^write$", CommandPattern: "^$"}, {Tool: "^write$", SkillPattern: "^$"},
	} {
		t.Run(fmt.Sprintf("native zero-value pattern/%v", spec), func(t *testing.T) {
			params := models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{spec}}
			f := newPreservedFixture(t, models.GraderKindToolConstraint, params,
				[]any{preservedEvent("write", 1, map[string]any{})}, nil)
			// Explicit {} is legitimate evidence. Preserve native digest-pattern
			// zero-value semantics without claiming an empty raw field existed.
			requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
			delete(f.event(0), "args")
			f.seal(t)
			require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
		})
	}
}

func TestPreservedMechanicalAdditionalNativeEvidenceBoundaries(t *testing.T) {
	t.Run("regex names versus exact allow-only names", func(t *testing.T) {
		for _, candidate := range []struct {
			params models.GraderParameters
			pass   bool
		}{
			{models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: `^write.*$`}}}, true},
			{models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: `^write.*$`}}}, true},
			{models.ToolConstraintGraderParameters{AllowOnly: &[]models.ToolSpecParameters{{Tool: `write.*`}}}, false},
			{models.ToolConstraintGraderParameters{AllowOnly: &[]models.ToolSpecParameters{{Tool: `WRITE-ALT`}}}, true},
		} {
			f := newPreservedFixture(t, models.GraderKindToolConstraint, candidate.params, []any{preservedEvent("write-alt", 1, nil)}, nil)
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, Observed, result.State, "%v", result.Err)
			require.Equal(t, candidate.pass, result.Result.Passed)
		}
	})
	t.Run("gapped and reordered sequences", func(t *testing.T) {
		for _, sequence := range []int{0, 1, 3} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{
				preservedEvent("read", 1, nil), preservedEvent("write", sequence, nil),
			}, nil)
			require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		}
	})
	t.Run("no result does not override explicit failed success", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{RequiredTools: []string{"write"}},
			[]any{preservedEvent("write", 1, nil)}, nil)
		event := f.event(0)
		event["success"], event["duration_ms"], event["tool_call_id"] = false, 0, ""
		f.seal(t)
		result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(gCtx *graders.Context) {
			require.False(t, gCtx.Session.ToolCalls[0].Success)
			require.False(t, gCtx.ToolEvents[0].Success)
			require.Nil(t, gCtx.Session.ToolCalls[0].Result)
			require.Nil(t, gCtx.ToolEvents[0].Result)
			require.Nil(t, gCtx.ToolEvents[0].Args)
		}})
		requirePreservedResult(t, result, true, 1, "passed")
	})
	t.Run("unavailable and source-digest-only tapes", func(t *testing.T) {
		for _, availability := range []string{"unavailable", "not_requested", "digest_only"} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MinCalls: new(1)}, []any{}, nil)
			artifact := &f.manifest.Artifacts[0]
			artifact.Availability, artifact.Completeness, artifact.Reason = availability, "unknown", "Synthetic content unavailable"
			artifact.Document, artifact.Pointer, artifact.ContentDigest = "", "", nil
			if availability == "digest_only" {
				artifact.SourceDigest = &models.EvidenceDigest{Encoding: "source-bytes", SHA256: strings.Repeat("a", 64)}
			}
			f.seal(t)
			require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
		}
	})
	t.Run("external argument schemas not assessed", func(t *testing.T) {
		for _, reference := range []string{"https://invalid.example/schema.json", "preserved_mechanical_test.go"} {
			f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{
				Tool: "^write$", Args: map[string]argmatcher.Matcher{"query": {
					Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": reference},
				}},
			}}}, []any{preservedEvent("write", 1, map[string]any{"query": "ready"})}, nil)
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, NotAssessed, result.State, "%v", result.Err)
			require.ErrorIs(t, result.Err, schemaloader.ErrExternalReference)
			require.Nil(t, result.Result)
		}
	})
	t.Run("required file missing and unsafe stored path", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"state.txt"}},
			[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", "ready")})
		f.root["workspaceFiles"] = []any{}
		f.seal(t)
		require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
		f = newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"../state.txt"}},
			[]any{}, []snapshot.WorkspaceFile{preservedFile("../state.txt", "ready")})
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("original symlink path is never reopened", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"state.txt"}},
			[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", "ready")})
		sentinel, err := os.MkdirTemp(".", ".preserved-source-")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(sentinel)) })
		require.NoError(t, os.Symlink("unavailable-original", filepath.Join(sentinel, "state.txt")))
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
		info, err := os.Lstat(filepath.Join(sentinel, "state.txt"))
		require.NoError(t, err)
		require.NotZero(t, info.Mode()&os.ModeSymlink)
	})
	t.Run("actual capture completeness remains unknown", func(t *testing.T) {
		for _, status := range []models.Status{models.StatusPassed, models.StatusFailed} {
			task := &models.TestCase{TestID: "native-task"}
			spec := &models.EvalSpec{Graders: []models.GraderConfig{{
				Identifier: "check", Kind: models.GraderKindToolCalls,
				Parameters: models.ToolCallsGraderParameters{RequiredTools: []string{"write"}},
			}}}
			snap, err := snapshot.Capture(snapshot.CaptureInput{
				EvalID: "native-eval", Task: task, Spec: spec, ExecutionMode: "mock",
				Engine: snapshot.SnapshotEngine{Type: "mock", ModelID: "synthetic"},
				Run: &models.RunResult{RunNumber: 1, Attempts: 1, Status: status,
					ToolEvents: []models.ToolEvent{{ToolName: "write", Sequence: 1, Turn: 1, Success: true}}},
			})
			require.NoError(t, err)
			reference, err := evidence.Reference(snap.Evidence, "tool-events")
			require.NoError(t, err)
			data, err := json.Marshal(snap)
			require.NoError(t, err)
			result := ObservePreservedMechanical(t.Context(), PreservedMechanicalInput{
				Task: task, Spec: spec, Check: models.RequirementCheck{Scope: "eval", Grader: "check"},
				SnapshotBytes: data, ManifestSHA256: snap.Evidence.SHA256, Evidence: []models.EvidenceReference{reference},
			})
			require.Equal(t, InsufficientEvidence, result.State, "%v", result.Err)
			require.Nil(t, result.Result)
		}
	})
	t.Run("selected declaration only and identity errors", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{RequiredTools: []string{"write"}},
			[]any{preservedEvent("write", 1, nil)}, nil)
		f.input.Spec.Graders = append(f.input.Spec.Graders, models.GraderConfig{
			Identifier: "not-selected", Kind: models.GraderKindProgram, Parameters: models.ProgramGraderParameters{Command: "never-run"},
		})
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
		original := f.input
		f.input.Spec = nil
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		f.input = original
		f.input.Task = nil
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		f.input = original
		f.input.Evidence = nil
		require.Equal(t, InsufficientEvidence, ObservePreservedMechanical(t.Context(), f.input).State)
		f.input = original
		f.input.Spec.Graders = append(f.input.Spec.Graders, f.input.Spec.Graders[0])
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("authored origin and profile never native", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{MaxCalls: new(0)}, []any{}, nil)
		f.manifest.Origin.EvalID = "reference:finite"
		f.encode(t)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
		f.manifest.Version = "1.1"
		f.encode(t)
		require.Equal(t, Invalid, ObservePreservedMechanical(t.Context(), f.input).State)
	})
	t.Run("native grader error remains operational", func(t *testing.T) {
		params := models.FileGraderParameters{MustExist: []string{"state.txt"}}
		f := newPreservedFixture(t, models.GraderKindFile, params, []any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", "ready")})
		f.input.Check.Scope = "task"
		f.input.Task.Validators = []models.ValidatorInline{{Identifier: "check", Kind: models.GraderKindFile, Parameters: params}}
		result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(gCtx *graders.Context) {
			// Inject a native Grade error only into detached evaluator state.
			preservedTestValue[models.FileGraderParameters](t, gCtx.TestCase.Validators[0].Parameters).MustExist[0] = "../escaped"
		}})
		require.Equal(t, OperationalError, result.State)
		require.ErrorContains(t, result.Err, "grading preserved evidence")
		require.Nil(t, result.Result)
		require.Equal(t, "state.txt", params.MustExist[0])
	})
	t.Run("valid supplied digest cannot replace actual native locator joins", func(t *testing.T) {
		for _, mutate := range []func(*preservedFixture){
			func(f *preservedFixture) {
				f.root["mirroredTape"] = f.root["toolEvents"]
				f.manifest.Artifacts[0].Pointer = "/mirroredTape"
			},
			func(f *preservedFixture) {
				f.manifest.Artifacts[2].Pointer = "/workspaceFiles/1"
			},
			func(f *preservedFixture) {
				f.manifest.Artifacts[2].ID = "foreign-file-id"
				f.input.Evidence[2].ArtifactID = "foreign-file-id"
			},
		} {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{RequiredTools: []string{"write"}},
				[]any{preservedEvent("write", 1, nil)}, []snapshot.WorkspaceFile{
					preservedFile("one.txt", "ready"), preservedFile("two.txt", "ready"),
				})
			mutate(f)
			f.seal(t)
			// Both the selected manifest and supplied artifact digests are valid;
			// only the ID/kind/exact raw pointer join exposes the substitution.
			require.NoError(t, evidence.ValidateNative(f.manifest))
			result := ObservePreservedMechanical(t.Context(), f.input)
			require.Equal(t, Invalid, result.State, "%v", result.Err)
			require.Nil(t, result.Result)
		}
	})
}

func TestPreservedMechanicalPaddedConstraintArgumentAdmission(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		params models.ToolConstraintGraderParameters
	}{
		{"expect", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: " write ", PathPattern: "^$"}}}},
		{"reject", models.ToolConstraintGraderParameters{RejectTools: []models.ToolSpecParameters{{Tool: " write ", PathPattern: "^$"}}}},
		{"allow-only", models.ToolConstraintGraderParameters{AllowOnly: &[]models.ToolSpecParameters{{Tool: " \tWRITE\n ", PathPattern: "^$"}}}},
		{"anchored-expect", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: " \t^write$\n ", PathPattern: "^$"}}}},
		{"anchored-reject", models.ToolConstraintGraderParameters{RejectTools: []models.ToolSpecParameters{{Tool: " \t^write$\n ", PathPattern: "^$"}}}},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			for _, args := range []struct {
				name  string
				value any
				state ObservationState
			}{
				{"absent", nil, InsufficientEvidence},
				{"null", json.RawMessage("null"), InsufficientEvidence},
				{"explicit empty object", map[string]any{}, Observed},
				{"nonobject", []any{}, OperationalError},
				{"known-field no coercion", map[string]any{"path": 7}, OperationalError},
			} {
				t.Run(args.name, func(t *testing.T) {
					f := newPreservedFixture(t, models.GraderKindToolConstraint, candidate.params,
						[]any{preservedEvent("write", 1, args.value)}, nil)
					result := ObservePreservedMechanical(t.Context(), f.input)
					require.Equal(t, args.state, result.State, "%v", result.Err)
					if args.state != Observed {
						require.Error(t, result.Err)
						require.Nil(t, result.Result)
						return
					}
					native, err := graders.Create("check", candidate.params)
					require.NoError(t, err)
					expected, err := native.Grade(t.Context(), &graders.Context{Session: &models.SessionDigest{
						ToolCallCount: 1, ToolsUsed: []string{"write"}, ToolCalls: []models.ToolCall{{Name: "write", Success: true}},
					}})
					require.NoError(t, err)
					require.Equal(t, expected.Passed, result.Result.Passed)
					require.Equal(t, expected.Score, result.Result.Score)
					require.Equal(t, expected.Feedback, result.Result.Feedback)
				})
			}
		})
	}
	t.Run("tool_calls does not inherit constraint trimming", func(t *testing.T) {
		f := newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{
			Expect: []models.ToolExpectation{{Tool: " write ", Args: map[string]argmatcher.Matcher{
				"path": {Kind: argmatcher.KindEquals, Equals: "ready"},
			}}},
		}, []any{preservedEvent("write", 1, nil)}, nil)
		requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), false, 0, "not satisfied")
	})
}

func TestPreservedMechanicalDirectReferencedConfigNotAssessed(t *testing.T) {
	for _, reference := range []string{"preset/check@v1", " ", "file:grader.yaml"} {
		for _, parameters := range []models.GraderParameters{
			models.ToolCallsGraderParameters{RequiredTools: []string{"write"}},
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "write", Args: map[string]argmatcher.Matcher{
				"path": {Kind: argmatcher.KindRegex, Regex: "["},
			}}}},
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "write", Args: map[string]argmatcher.Matcher{
				"path": {Kind: argmatcher.KindEquals, Equals: make(chan int)},
			}}}},
			nil,
		} {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{RequiredTools: []string{"write"}}, []any{preservedEvent("write", 1, nil)}, nil)
			f.input.Spec.Graders[0].Ref = reference
			f.input.Spec.Graders[0].Parameters = parameters
			result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) {
				t.Fatal("referenced configuration must not reach grading preparation")
			}})
			require.Equal(t, NotAssessed, result.State, "%v", result.Err)
			require.ErrorContains(t, result.Err, "referenced native graders")
			require.Nil(t, result.Result)
		}
	}
}

func TestPreservedMechanicalOriginalUnicodeEscapeAdmission(t *testing.T) {
	// Bind synthetic manifests to Go's replacement-decoded values first. Lossy
	// decoding would otherwise satisfy the digests and produce native verdicts.
	for _, source := range []string{"event args", "file content"} {
		for _, escape := range []string{`\ud800`, `\udfff`, `\ud800\u0061`, `\ud800\ud800`} {
			t.Run(source+"/"+escape, func(t *testing.T) {
				var normalized string
				require.NoError(t, json.Unmarshal([]byte(`"`+escape+`"`), &normalized))
				var f *preservedFixture
				var needle, replacement []byte
				if source == "event args" {
					f = newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{
						Expect: []models.ToolExpectation{{Tool: "write", Args: map[string]argmatcher.Matcher{
							"query": {Kind: argmatcher.KindEquals, Equals: normalized},
						}}},
					}, []any{preservedEvent("write", 1, map[string]any{"query": normalized})}, nil)
					needle, replacement = []byte(`"query":"`+normalized+`"`), []byte(`"query":"`+escape+`"`)
				} else {
					f = newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"state.txt"}},
						[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", normalized)})
					needle, replacement = []byte(`"content":"`+normalized+`"`), []byte(`"content":"`+escape+`"`)
				}
				require.Contains(t, string(f.input.SnapshotBytes), string(needle))
				f.input.SnapshotBytes = bytes.Replace(f.input.SnapshotBytes, needle, replacement, 1)
				require.True(t, json.Valid(f.input.SnapshotBytes))
				result := observePreservedMechanical(t.Context(), f.input, preservedMechanicalHooks{prepared: func(*graders.Context) {
					t.Fatal("malformed Unicode must not be decoded or materialized")
				}})
				require.Equal(t, Invalid, result.State, "%v", result.Err)
				require.ErrorContains(t, result.Err, "Unicode")
				require.Nil(t, result.Result)
			})
		}
	}
	for _, text := range []string{"😀", `\ud800`} {
		for _, source := range []string{"event args", "file content"} {
			t.Run("valid/"+source+"/"+text, func(t *testing.T) {
				var f *preservedFixture
				if source == "event args" {
					f = newPreservedFixture(t, models.GraderKindToolCalls, models.ToolCallsGraderParameters{
						Expect: []models.ToolExpectation{{Tool: "write", Args: map[string]argmatcher.Matcher{
							"query": {Kind: argmatcher.KindEquals, Equals: text},
						}}},
					}, []any{preservedEvent("write", 1, map[string]any{"query": text})}, nil)
				} else {
					f = newPreservedFixture(t, models.GraderKindFile, models.FileGraderParameters{MustExist: []string{"state.txt"}},
						[]any{}, []snapshot.WorkspaceFile{preservedFile("state.txt", text)})
				}
				if text == "😀" {
					f.input.SnapshotBytes = bytes.Replace(f.input.SnapshotBytes, []byte(text), []byte(`\ud83d\ude00`), 1)
				}
				requirePreservedResult(t, ObservePreservedMechanical(t.Context(), f.input), true, 1, "passed")
			})
		}
	}
	t.Run("truncated escapes cannot panic before Unicode validation", func(t *testing.T) {
		for _, malformed := range []string{`{"x":"\`, `{"x":"\u`, `{"x":"\ud8`, `{"x":"\uZZZZ"}`} {
			f := newPreservedFixture(t, models.GraderKindToolCalls,
				models.ToolCallsGraderParameters{MaxCalls: new(0)}, []any{}, nil)
			f.input.SnapshotBytes = []byte(malformed)
			require.NotPanics(t, func() {
				result := ObservePreservedMechanical(t.Context(), f.input)
				require.Equal(t, Invalid, result.State)
				require.Nil(t, result.Result)
			})
		}
	})
}
