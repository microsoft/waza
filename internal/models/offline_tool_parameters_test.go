package models

import (
	"testing"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOfflineToolParametersRetainBlockedMatchers(t *testing.T) {
	const ref = "https://never-read.invalid/schema"
	for _, kind := range []GraderKind{GraderKindToolCalls, GraderKindToolConstraint} {
		t.Run(string(kind), func(t *testing.T) {
			config := map[string]any{}
			external := map[string]any{"json_schema": map[string]any{"$ref": ref}}
			literal := map[string]any{"equals": map[string]any{"$ref": ref}}
			args := map[string]any{"schema": external, "payload": literal}
			if kind == GraderKindToolCalls {
				config["required_tools"], config["forbidden_tools"] = []string{"read"}, []string{"write"}
				config["min_calls"], config["max_calls"] = 0, 4
				config["expect"] = []any{map[string]any{"tool": "read", "args": args}, map[string]any{"tool": "write"}}
			} else {
				config["expect_tools"] = []any{map[string]any{"tool": "read", "command_pattern": "^cmd", "skill_pattern": "^skill", "path_pattern": "^path", "args": args}}
				config["reject_tools"] = []any{map[string]any{"tool": "write"}}
				config["allow_only"] = []any{map[string]any{"tool": "["}}
			}
			data, err := yaml.Marshal(map[string]any{"type": kind, "name": "retained", "config": config})
			require.NoError(t, err)
			original := append([]byte(nil), data...)
			var declaration schemaGuardGrader
			require.NoError(t, yaml.Unmarshal(data, &declaration))
			params, err := decodeOfflineToolParameters(declaration)
			require.Error(t, err)
			invalid, externalError := schemaloader.ClassifyError(err)
			require.False(t, invalid)
			require.True(t, externalError)
			var matchers map[string]argmatcher.Matcher
			switch p := params.(type) {
			case ToolCallsGraderParameters:
				require.Equal(t, []string{"read"}, p.RequiredTools)
				require.Equal(t, []string{"write"}, p.ForbiddenTools)
				require.NotNil(t, p.MinCalls)
				require.NotNil(t, p.MaxCalls)
				require.Equal(t, 0, *p.MinCalls)
				require.Equal(t, 4, *p.MaxCalls)
				require.Equal(t, "read", p.Expect[0].Tool)
				require.Equal(t, "write", p.Expect[1].Tool)
				matchers = p.Expect[0].Args
			case ToolConstraintGraderParameters:
				require.Equal(t, "read", p.ExpectTools[0].Tool)
				require.Equal(t, "^cmd", p.ExpectTools[0].CommandPattern)
				require.Equal(t, "^skill", p.ExpectTools[0].SkillPattern)
				require.Equal(t, "^path", p.ExpectTools[0].PathPattern)
				require.Equal(t, "write", p.RejectTools[0].Tool)
				require.NotNil(t, p.AllowOnly)
				require.Equal(t, "[", (*p.AllowOnly)[0].Tool)
				matchers = p.ExpectTools[0].Args
			default:
				t.Fatalf("unexpected parameters %T", params)
			}
			require.Equal(t, argmatcher.KindJSONSchema, matchers["schema"].Kind)
			require.Equal(t, map[string]any{"$ref": ref}, matchers["schema"].JSONSchema)
			require.Equal(t, argmatcher.KindEquals, matchers["payload"].Kind)
			require.Equal(t, map[string]any{"$ref": ref}, matchers["payload"].Equals)
			require.Equal(t, original, data)
		})
	}
}

func TestOfflineToolParametersPreserveAllowOnlyPresence(t *testing.T) {
	for _, config := range []string{"{}", "{allow_only: null}", "{allow_only: []}"} {
		t.Run(config, func(t *testing.T) {
			var declaration schemaGuardGrader
			require.NoError(t, yaml.Unmarshal([]byte("type: tool_constraint\nconfig: "+config), &declaration))
			params, err := decodeOfflineToolParameters(declaration)
			require.NoError(t, err)
			typed, ok := params.(ToolConstraintGraderParameters)
			require.True(t, ok)
			if config == "{allow_only: []}" {
				require.NotNil(t, typed.AllowOnly)
				require.Empty(t, *typed.AllowOnly)
			} else {
				require.Nil(t, typed.AllowOnly)
			}
		})
	}
}

func TestOfflineToolParametersRetainDecodeFailures(t *testing.T) {
	for _, document := range []string{
		"type: text\nconfig: {}",
		"type: tool_calls\nconfig: {min_calls: invalid}",
		"type: tool_calls\nconfig: {expect: [17]}",
		"type: tool_calls\nconfig: {expect: [{tool: read, args: [invalid]}]}",
		"type: tool_constraint\nconfig: {allow_only: invalid}",
		"type: tool_constraint\nconfig: {expect_tools: [17]}",
		"type: tool_constraint\nconfig: {reject_tools: [{tool: read, args: [invalid]}]}",
		"type: tool_constraint\nconfig: {allow_only: [{tool: read, args: {value: {unknown: invalid}}}]}",
	} {
		t.Run(document, func(t *testing.T) {
			var declaration schemaGuardGrader
			require.NoError(t, yaml.Unmarshal([]byte(document), &declaration))
			_, err := decodeOfflineToolParameters(declaration)
			require.Error(t, err)
			invalid, external := schemaloader.ClassifyError(err)
			require.True(t, invalid)
			require.False(t, external)
		})
	}
}
