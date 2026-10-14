package graders

import (
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
)

type staticFieldTestLoader func(string) (any, error)

func (f staticFieldTestLoader) Load(url string) (any, error) { return f(url) }

func TestToolStaticFieldsPrecedeSchemaLoading(t *testing.T) {
	externalArgs := func() map[string]argmatcher.Matcher {
		return map[string]argmatcher.Matcher{"value": {
			Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": "https://never-read.invalid/schema"},
		}}
	}
	negative := -1
	emptyAllow := []models.ToolSpecParameters{}
	literalAllow := []models.ToolSpecParameters{{Tool: "[", Args: externalArgs()}}
	badAllow := []models.ToolSpecParameters{{Tool: "write", PathPattern: "["}}
	for _, test := range []struct {
		name    string
		params  models.GraderParameters
		invalid bool
	}{
		{"later tool_calls tool", models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: externalArgs()}, {Tool: "["}}}, true},
		{"tool_calls bounds", models.ToolCallsGraderParameters{MinCalls: &negative, Expect: []models.ToolExpectation{{Tool: "read", Args: externalArgs()}}}, true},
		{"same tool_calls entry", models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: " ", Args: externalArgs()}}}, true},
		{"later constraint entry", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "read", Args: externalArgs()}, {Tool: "write", CommandPattern: "["}}}, true},
		{"later reject group", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "read", Args: externalArgs()}}, RejectTools: []models.ToolSpecParameters{{Tool: "write", SkillPattern: "["}}}, true},
		{"later allow group", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "read", Args: externalArgs()}}, AllowOnly: &badAllow}, true},
		{"tool_calls external only", models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: externalArgs()}}}, false},
		{"literal allow names", models.ToolConstraintGraderParameters{AllowOnly: &literalAllow}, false},
		{"deny all plus external", models.ToolConstraintGraderParameters{ExpectTools: []models.ToolSpecParameters{{Tool: "read", Args: externalArgs()}}, AllowOnly: &emptyAllow}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var loads int
			loader := staticFieldTestLoader(func(string) (any, error) {
				loads++
				return nil, schemaloader.ErrExternalReference
			})
			var err error
			switch p := test.params.(type) {
			case models.ToolCallsGraderParameters:
				_, err = newToolCallsGraderWithLoader("test", p, loader)
			case models.ToolConstraintGraderParameters:
				_, err = newToolConstraintGraderWithLoader("test", p, loader)
			default:
				t.Fatalf("unexpected parameters %T", test.params)
			}
			require.Error(t, err)
			invalid, external := schemaloader.ClassifyError(err)
			require.Equal(t, test.invalid, invalid)
			require.Equal(t, !test.invalid, external)
			if test.invalid {
				require.Zero(t, loads, "non-matcher fields must be validated before schema loading")
			} else {
				require.Positive(t, loads, "valid fields must retain the supplied loader policy")
			}
			invalid, external = schemaloader.ClassifyError(ValidateConfig("test", test.params))
			require.Equal(t, test.invalid, invalid)
			require.Equal(t, !test.invalid, external)
		})
	}
}
