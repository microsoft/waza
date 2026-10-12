package jsonrpc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/scaffold"
	"github.com/stretchr/testify/require"
)

func TestScenarioAPI(t *testing.T) {
	dir := t.TempDir()
	files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
	require.NoError(t, err)
	path := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(path, []byte(files["eval.yaml"]), 0o600))
	server := newTestServer()
	resp := rpcCall(t, server, "eval.get", map[string]string{"path": path})
	require.Nil(t, resp.Error)
	data, err := json.Marshal(resp.Result)
	require.NoError(t, err)
	var result EvalGetResult
	require.NoError(t, json.Unmarshal(data, &result))
	require.Equal(t, "inventory", result.Scenario)
	require.Empty(t, result.SkillName)
	resp = rpcCall(t, server, "eval.list", map[string]string{"dir": dir})
	require.Nil(t, resp.Error)
	data, err = json.Marshal(resp.Result)
	require.NoError(t, err)
	var list EvalListResult
	require.NoError(t, json.Unmarshal(data, &list))
	require.Len(t, list.Evals, 1)
	require.Equal(t, "inventory", list.Evals[0].Scenario)
	resp = rpcCall(t, server, "eval.validate", map[string]string{"path": path})
	require.Nil(t, resp.Error)
	data, err = json.Marshal(resp.Result)
	require.NoError(t, err)
	var valid EvalValidateResult
	require.NoError(t, json.Unmarshal(data, &valid))
	require.True(t, valid.Valid)
}

func TestScenarioAPIOptionalSkillAndLegacyOmission(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) {
			dir := t.TempDir()
			files, err := scaffold.ScenarioFiles("inventory", "model", "eval.yaml", "tasks/*.yaml", ".yaml", "repository")
			require.NoError(t, err)
			eval := files["eval.yaml"] + "skill: reader\n"
			if legacy {
				eval = strings.ReplaceAll(eval, "scenario: \"inventory\"\n", "")
				eval = strings.ReplaceAll(eval, "schemaVersion: \"2.0\"", "schemaVersion: \"1.4\"")
			}
			path := filepath.Join(dir, "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
			server := newTestServer()
			resp := rpcCall(t, server, "eval.get", map[string]string{"path": path})
			require.Nil(t, resp.Error)
			data, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &wire))
			require.JSONEq(t, `"reader"`, string(wire["skill"]))
			if legacy {
				require.NotContains(t, wire, "scenario")
			} else {
				require.JSONEq(t, `"inventory"`, string(wire["scenario"]))
			}
		})
	}
}
