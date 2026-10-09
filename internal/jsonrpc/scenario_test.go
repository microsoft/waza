package jsonrpc

import (
	"encoding/json"
	"os"
	"path/filepath"
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
