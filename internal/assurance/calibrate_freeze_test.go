package assurance

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCalibrationParametersCopyKeepsMatchersDeclarative(t *testing.T) {
	var loads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		loads.Add(1)
		_, err := w.Write([]byte(`{"type":"string"}`))
		if err != nil {
			t.Errorf("writing sentinel schema: %v", err)
		}
	}))
	defer server.Close()
	for _, location := range []string{"calls", "expect", "reject", "allow", "calls pointer", "constraint pointer"} {
		t.Run(location, func(t *testing.T) {
			nested := []any{json.Number("9007199254740993")}
			literal := map[string]any{"nested": nested}
			bound := 1.0
			args := map[string]argmatcher.Matcher{
				"literal": {Kind: argmatcher.KindEquals, Equals: literal},
				"schema":  {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": server.URL}},
				"file":    {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": testutil.FileURL(filepath.Join(t.TempDir(), "absent.json"))}},
				"regex":   {Kind: argmatcher.KindRegex, Regex: "["},
				"range":   {Kind: argmatcher.KindRange, Range: &argmatcher.RangeSpec{GTE: &bound}},
			}
			calls := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: args}}}
			specs := []models.ToolSpecParameters{{Tool: "read", Args: args}}
			constraints := models.ToolConstraintGraderParameters{}
			var source models.GraderParameters
			switch location {
			case "calls":
				source = calls
			case "calls pointer":
				source = &calls
			case "expect":
				constraints.ExpectTools = specs
				source = constraints
			case "reject":
				constraints.RejectTools = specs
				source = constraints
			case "allow", "constraint pointer":
				constraints.AllowOnly = &specs
				source = constraints
				if location == "constraint pointer" {
					source = &constraints
				}
			}
			before := marshalReferenceTest(t, source)
			copied, err := calibrationParametersCopy(source)
			require.NoError(t, err)
			require.IsType(t, source, copied)
			require.Equal(t, before, marshalReferenceTest(t, copied))
			require.Zero(t, loads.Load())
			nested[0] = "changed"
			args["schema"].JSONSchema["$ref"] = "changed"
			bound = 2
			args["extra"] = argmatcher.Matcher{Kind: argmatcher.KindContains, Contains: "changed"}
			require.Equal(t, before, marshalReferenceTest(t, copied), "copied matchers must not alias source maps, slices or bounds")
		})
	}
}

func TestCalibrationParametersCopyMatcherBoundaries(t *testing.T) {
	for _, source := range []models.GraderParameters{
		nil, (*models.ToolCallsGraderParameters)(nil), (*models.ToolConstraintGraderParameters)(nil),
		models.ToolCallsGraderParameters{},
		models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{}},
		models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read"}}},
		models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: map[string]argmatcher.Matcher{}}}},
	} {
		copied, err := calibrationParametersCopy(source)
		require.NoError(t, err)
		require.Equal(t, source, copied)
	}
	for _, allow := range []*[]models.ToolSpecParameters{nil, new([]models.ToolSpecParameters(nil)), new([]models.ToolSpecParameters{})} {
		source := models.ToolConstraintGraderParameters{AllowOnly: allow}
		copied, err := calibrationParametersCopy(source)
		require.NoError(t, err)
		require.Equal(t, source, copied)
		if allow != nil {
			parameters, ok := copied.(models.ToolConstraintGraderParameters)
			require.True(t, ok)
			require.NotSame(t, allow, parameters.AllowOnly)
		}
	}
	matcher := argmatcher.Matcher{Kind: argmatcher.KindRegex, Regex: "^ready$"}
	require.NoError(t, matcher.Compile())
	matcher.Equals = math.NaN() // Inactive fields are outside native JSON identity.
	source := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: map[string]argmatcher.Matcher{"value": matcher}}}}
	copied, err := calibrationParametersCopy(source)
	require.NoError(t, err)
	parameters, ok := copied.(models.ToolCallsGraderParameters)
	require.True(t, ok)
	target := parameters.Expect[0].Args["value"]
	require.False(t, target.IsCompiled(), "freezing declarations must not retain runtime compilation caches")
	require.True(t, matcher.IsCompiled())
}

func TestCalibrationParametersCopyRejectsInvalidMatcherJSON(t *testing.T) {
	for _, matcher := range []argmatcher.Matcher{
		{Kind: argmatcher.KindEquals, Equals: math.NaN()},
		{Kind: argmatcher.KindEquals, Equals: json.RawMessage(`{"duplicate":1,"duplicate":2}`)},
		{Kind: "unknown"},
	} {
		args := map[string]argmatcher.Matcher{"invalid": matcher}
		specs := []models.ToolSpecParameters{{Tool: "read", Args: args}}
		for _, source := range []models.GraderParameters{
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: args}}},
			models.ToolConstraintGraderParameters{ExpectTools: specs},
			models.ToolConstraintGraderParameters{RejectTools: specs},
			models.ToolConstraintGraderParameters{AllowOnly: &specs},
		} {
			_, err := calibrationParametersCopy(source)
			require.ErrorContains(t, err, `copy matcher argument "invalid"`)
		}
	}
}

func TestCalibrateUnrelatedMatchersDoNotLoadOrLoseNumbers(t *testing.T) {
	var loads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		loads.Add(1)
		_, err := w.Write([]byte(`{"type":"string"}`))
		if err != nil {
			t.Errorf("writing sentinel schema: %v", err)
		}
	}))
	defer server.Close()
	for _, location := range []string{"eval", "task", "checkpoint"} {
		for _, gate := range []string{"selected", "not selected", "not reviewed"} {
			t.Run(location+"/"+gate, func(t *testing.T) {
				request, document, _, harness := calibrationFixture(t)
				args := map[string]argmatcher.Matcher{
					"literal": {Kind: argmatcher.KindEquals, Equals: json.Number("9007199254740993")},
					"schema":  {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": server.URL, "const": json.Number("9007199254740993")}},
				}
				parameters := models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: args}}}
				inline := models.ValidatorInline{Identifier: "unrelated", Kind: models.GraderKindToolCalls, Parameters: parameters}
				switch location {
				case "eval":
					request.Spec.Graders = append(request.Spec.Graders, models.GraderConfig{Identifier: inline.Identifier, Kind: inline.Kind, Parameters: parameters})
				case "task":
					request.Tasks["task"].Validators = append(request.Tasks["task"].Validators, inline)
				case "checkpoint":
					request.Tasks["task"].Checkpoints = append(request.Tasks["task"].Checkpoints, models.Checkpoint{AfterTurn: 1, Graders: []models.ValidatorInline{inline}})
				}
				calibrationRebind(t, &request.VerifyRequest, &document)
				switch gate {
				case "not selected":
					request.Calibrate = false
				case "not reviewed":
					request.Review.Decision = nil
				}
				frozen, err := freezeCalibrationRequest(request.VerifyRequest)
				require.NoError(t, err)
				require.Equal(t, marshalReferenceTest(t, request.Spec), marshalReferenceTest(t, frozen.Spec))
				require.Equal(t, marshalReferenceTest(t, request.Tasks), marshalReferenceTest(t, frozen.Tasks))
				report, err := Calibrate(t.Context(), request)
				require.NoError(t, err)
				requireCalibrationReport(t, report)
				if gate == "selected" {
					require.Equal(t, AssessmentPassed, report.State)
					require.Equal(t, 4, harness.factories)
				} else {
					require.Equal(t, AssessmentNotAssessed, report.Calibration.State)
					require.Zero(t, harness.factories)
					require.Zero(t, harness.notices)
					if gate == "not selected" {
						require.Equal(t, "calibration_not_selected", report.Calibration.Reason)
					}
				}
				require.Zero(t, loads.Load(), "even unselected declarations must not fetch matcher references")
			})
		}
	}
}
