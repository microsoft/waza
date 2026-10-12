package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestReleaseAPICompletedAndInterruptedOfflineCollection(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable release collection unsupported")
	}
	root := testutil.RepoFile(t, "examples", "controlled-comparison")
	read := func(name string) []byte {
		data, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		return data
	}
	design, err := releasepolicy.DecodeDesignInput(read("design.json"))
	require.NoError(t, err)
	requirements, err := releasepolicy.DecodeRequirementsInput(read("requirements.json"))
	require.NoError(t, err)
	source := controlledcomparison.Source{EvalPath: filepath.Join(root, "eval.yaml")}
	rawPolicy, err := controlledcomparison.Plan(source, source, design, requirements, nil)
	require.NoError(t, err)
	policy, err := releasepolicy.DecodePolicy(rawPolicy)
	require.NoError(t, err)
	input, output, credits := json.Number("9007199254740993"), json.Number("9007199254740995"), json.Number("0.125")
	for _, interrupted := range []bool{false, true} {
		name := "completed"
		if interrupted {
			name = "interrupted"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			collection := filepath.Join(dir, name)
			starts, stops, attempts := 0, 0, 0
			stop := errors.New("synthetic offline interruption")
			collector := releasepolicy.Collector{
				Initialize: func(context.Context) error { starts++; return nil },
				Shutdown:   func(context.Context) error { stops++; return nil },
				Usage: func(context.Context, releasepolicy.Arm) ([]releasepolicy.UsageAxis, error) {
					return []releasepolicy.UsageAxis{
						{Axis: "input_tokens", Availability: "available", Value: &input, Observation: "final_complete_attributable"},
						{Axis: "output_tokens", Availability: "available", Value: &output, Observation: "final_complete_attributable"},
						{Axis: "ai_credits", Availability: "available", Value: &credits, Observation: "final_complete_attributable"},
					}, nil
				},
				Attempt: func(_ context.Context, key releasepolicy.AttemptKey) (releasepolicy.AttemptObservation, error) {
					attempts++
					if interrupted && attempts == 2 {
						return releasepolicy.AttemptObservation{}, stop
					}
					settings := policy.Arms[key.Arm].Plan.Tasks[0].Settings
					origin := models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID,
						RunNumber: key.Trial, AttemptCount: key.Attempt}
					return releasepolicy.AttemptObservation{
						Summary: releasepolicy.AttemptSummary{
							Key: key, Origin: origin, Status: "passed", Category: "behavioral",
							Checks: []releasepolicy.CheckSummary{{Scope: "task", Grader: "synthetic-check",
								Passed: true, Score: "1", OperationalState: "observed"}},
							References: []models.EvidenceReference{},
							Runtime: releasepolicy.RuntimeObservation{TaskID: key.TaskID,
								RequestedEngine: settings.Engine, RequestedModel: settings.Model,
								RequestedReasoning: settings.ReasoningEffort, Availability: "unavailable",
								Reason: "Synthetic offline observations have no live runtime claim."},
						},
						Result: releasepolicy.ActualRunRow{Origin: origin, Run: models.RunResult{
							RunNumber: key.Trial, Attempts: key.Attempt, Status: models.StatusPassed,
							Validations: map[string]models.GraderResults{"synthetic-check": {
								Name: "synthetic-check", Passed: true, Score: 1,
							}},
						}},
					}, nil
				},
			}
			err := releasepolicy.Collect(t.Context(), rawPolicy, collection, collector)
			if interrupted {
				require.ErrorIs(t, err, stop)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, starts)
			require.Equal(t, 1, stops)
			expected, err := releasepolicy.ReadDecision(collection)
			readError := ""
			if interrupted {
				require.Error(t, err)
				readError = err.Error()
			} else {
				require.NoError(t, err)
			}
			mux := http.NewServeMux()
			RegisterReleaseRoutes(mux, dir)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
			require.Equal(t, http.StatusOK, response.Code, "%s", response.Body.String())
			var collections []ReleaseCollection
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &collections))
			require.Len(t, collections, 1)
			item := collections[0]
			require.Equal(t, name, item.Path)
			require.Equal(t, readError, item.Error)
			var rawCollections []struct {
				Decision json.RawMessage `json:"decision"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &rawCollections))
			require.Len(t, rawCollections, 1)
			wire := rawCollections[0].Decision
			want, err := json.Marshal(releaseView(expected))
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(wire), "API must preserve the independently read decision")
			validateReleaseViewSchema(t, wire)
			if interrupted {
				require.False(t, item.Decision.Accepted)
				require.Equal(t, "partial", item.Decision.Completeness.State)
				started, complete := 0, 0
				for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
					counts := item.Decision.Accounting[arm]
					require.Equal(t, 2, counts.PlannedTrials)
					require.Nil(t, counts.FirstRate)
					require.Nil(t, counts.RetryRate)
					started += counts.StartedAttempts
					complete += counts.CompleteAttempts
				}
				require.Equal(t, 2, started)
				require.Equal(t, 1, complete)
			} else {
				require.Equal(t, "complete", item.Decision.Completeness.State)
				require.False(t, item.Decision.Accepted, "the example's one unjustified cluster is not a quality claim")
				require.Equal(t, 4, attempts)
				for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
					counts := item.Decision.Accounting[arm]
					require.Equal(t, releasepolicy.Reliability{
						PlannedTrials: 2, StartedTrials: 2, CompleteTrials: 2, Attempts: 2,
						StartedAttempts: 2, CompleteAttempts: 2, FirstPasses: 2, RetryPasses: 2,
						FirstRate: counts.FirstRate, RetryRate: counts.RetryRate,
					}, counts)
					require.NotNil(t, counts.FirstRate)
					require.NotNil(t, counts.RetryRate)
					require.Equal(t, 1.0, *counts.FirstRate)
					require.Equal(t, 1.0, *counts.RetryRate)
					require.Len(t, item.Decision.Usage[arm], 3)
					for i, token := range []string{input.String(), output.String(), credits.String()} {
						axis := item.Decision.Usage[arm][i]
						require.Equal(t, []string{"input_tokens", "output_tokens", "ai_credits"}[i], axis.Axis)
						require.NotNil(t, axis.Value)
						require.Equal(t, token, *axis.Value)
						require.Equal(t, "available", axis.Availability)
						require.Equal(t, "final_complete_attributable", axis.Observation)
					}
				}
			}
		})
	}
}

func TestReleaseRateSchemaUnavailableVersusZero(t *testing.T) {
	var definition map[string]any
	require.NoError(t, json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &definition))
	delete(definition, "oneOf")
	definition["$ref"] = "#/$defs/releasepolicy_Reliability"
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("reliability.json", definition))
	contract, err := compiler.Compile("reliability.json")
	require.NoError(t, err)
	for _, field := range []string{"first_attempt_rate", "retry_policy_rate"} {
		for _, tc := range []struct {
			name    string
			value   any
			missing bool
			valid   bool
		}{
			{name: "unavailable", value: nil, valid: true},
			{name: "observed_zero", value: float64(0), valid: true},
			{name: "observed_one", value: float64(1), valid: true},
			{name: "string_zero", value: "0"},
			{name: "boolean", value: false},
			{name: "array", value: []any{}},
			{name: "missing", missing: true},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				data, err := json.Marshal(releasepolicy.Reliability{})
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(data, &payload))
				payload[field] = tc.value
				if tc.missing {
					delete(payload, field)
				}
				err = contract.Validate(payload)
				if tc.valid {
					require.NoError(t, err)
					require.Equal(t, tc.value, payload[field], "unknown must not become zero")
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

func TestReleaseArtifactIsolation(t *testing.T) {
	dir := t.TempDir()
	legacy, err := os.ReadFile(testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "results-1.0.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"waza.release-policy", "waza.release-resolved-plan", "waza.release-receipt",
		"waza.release-journal", "waza.release-results", "waza.release-result-binding", "waza.release-decision",
		"waza.release-future"} {
		data := `{"kind":"` + kind + `","tasks":[],"eval_id":"not-a-run","version":"1.0"}`
		if err := os.WriteFile(filepath.Join(dir, kind+".json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := NewFileStore(dir).ListRuns("", "")
	if err != nil || len(runs) != 1 || runs[0].ID != "compatibility-run" {
		t.Fatalf("strict sidecars contaminated ordinary outcomes: %+v %v", runs, err)
	}
}

func TestReleaseAPIMissingInvalidAndSymlink(t *testing.T) {
	for _, name := range []string{"empty", "invalid", "symlink", "missing_root"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mux := http.NewServeMux()
			switch name {
			case "invalid":
				if err := os.WriteFile(filepath.Join(dir, "policy.json"), []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(t.TempDir(), "outside.json")
				if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "policy.json")); err != nil {
					t.Skipf("symlinks unsupported: %v", err)
				}
			case "missing_root":
				dir = filepath.Join(dir, "missing")
			}
			RegisterReleaseRoutes(mux, dir)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
			want := http.StatusOK
			if name == "symlink" || name == "missing_root" {
				want = http.StatusInternalServerError
			}
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if name == "invalid" {
				var collections []ReleaseCollection
				if err := json.Unmarshal(response.Body.Bytes(), &collections); err != nil {
					t.Fatal(err)
				}
				if len(collections) != 1 || collections[0].Decision.Accepted || collections[0].Error == "" ||
					collections[0].Decision.Compatibility.State != "invalid" {
					t.Fatalf("invalid collection looked successful: %+v", collections)
				}
			}
		})
	}
}

func TestReleaseUsageRetainsExactTokens(t *testing.T) {
	value := json.Number("9007199254740993")
	d := releasepolicy.InitialDecision()
	d.Usage[releasepolicy.Baseline] = []releasepolicy.UsageAxis{{Axis: "input_tokens",
		Availability: "available", Value: &value, Observation: "final_complete_attributable"}}
	data, err := json.Marshal(releaseView(d))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"value":"9007199254740993"`) {
		t.Fatalf("JavaScript transport lost exact token count: %s", data)
	}
	validateReleaseViewSchema(t, data)
}

func validateReleaseViewSchema(t *testing.T, data []byte) {
	t.Helper()
	var definition, view any
	if err := json.Unmarshal([]byte(schemas.ReleaseArtifactsSchemaJSON), &definition); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("release.json", definition); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("release.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(view); err != nil {
		t.Fatalf("exact-number API view disagrees with its published schema: %v", err)
	}
}
