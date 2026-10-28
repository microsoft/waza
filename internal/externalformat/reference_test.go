package externalformat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPinnedReferenceSourceBytes(t *testing.T) {
	for name, digest := range map[string]string{
		"eval-file.schema.ts":               "ce8d433fac359995b1af8f12f12afc9477cdd386b0bd90409804758075705ea8",
		"yaml-loader.ts":                    "cd2460bd31c5ebda01282a756f9a57bf0a33af77e5e9a3d0b785f17186d8a29e",
		"assertions.ts":                     "152446e80042b29597eb9d124860a6eb67587be1d642b50c101af3711c9ecb02",
		"scoring.ts":                        "167936782bc74f93ccef9b026a94da46ca0a53759dd2f90a3df0ffcf2acbf59e",
		"upstream-deterministic-suite.yaml": "ed93fed3aef2785399ea96c93e918ceba8d6557fc1ce637ccbb418349d8f70ca",
		"LICENSE.agentv":                    "a310bb09289f002d4d27912f9254926afb9510ce2289b6c6154816ab21fc577f",
		"package-lock.json":                 "5fb1a95369f321ba66c09e74cd13735daa4aa3541d8e7f1670ebaff191286b73",
		"observed.json":                     "f2217540d8027d3973e751d2cde725d087e0eab0830c1b973b2a7ffe0d3edfa5",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/reference/" + name)
			require.NoError(t, err)
			sum := sha256.Sum256(data)
			require.Equal(t, digest, hex.EncodeToString(sum[:]))
		})
	}
	data, err := os.ReadFile("testdata/contains.eval.yaml")
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	require.Equal(t, "dc8ecfcf0e3e57b33f84388c898989f78f21b0f23a3477978d3f1e584276fe74", hex.EncodeToString(sum[:]))
}

func TestCanonicalSourceManifest(t *testing.T) {
	data, err := os.ReadFile("testdata/reference/sources.json")
	require.NoError(t, err)
	var manifest struct {
		Commit string `json:"commit"`
		Files  []struct {
			File   string `json:"file"`
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal(data, &manifest))
	require.Equal(t, "68df6d2206cea61f721d0445b405efcb5e94aff0", manifest.Commit)
	paths := map[string]string{
		"eval-file.schema.ts":               "packages/core/src/evaluation/validation/eval-file.schema.ts",
		"yaml-loader.ts":                    "packages/core/src/evaluation/yaml-loader.ts",
		"assertions.ts":                     "packages/core/src/evaluation/graders/assertions.ts",
		"scoring.ts":                        "packages/core/src/evaluation/graders/scoring.ts",
		"upstream-deterministic-suite.yaml": "examples/features/deterministic-graders/evals/suite.yaml",
		"LICENSE.agentv":                    "LICENSE",
	}
	require.Len(t, manifest.Files, len(paths))
	for _, file := range manifest.Files {
		path, ok := paths[file.File]
		require.True(t, ok)
		require.Equal(t, "https://raw.githubusercontent.com/EntityProcess/agentv/"+manifest.Commit+"/"+path, file.URL)
		source, err := os.ReadFile("testdata/reference/" + file.File)
		require.NoError(t, err)
		digest := sha256.Sum256(source)
		require.Equal(t, hex.EncodeToString(digest[:]), file.SHA256)
		delete(paths, file.File)
	}
	require.Empty(t, paths)
}

func TestRealUpstreamSuiteRejectedWithoutExtraction(t *testing.T) {
	data, err := os.ReadFile("testdata/reference/upstream-deterministic-suite.yaml")
	require.NoError(t, err)
	require.Contains(t, string(data), "type: contains")
	require.Contains(t, string(data), "providers:")
	require.Contains(t, string(data), "Response mentions")
	suite, err := Import(data, selected)
	require.Error(t, err)
	require.Nil(t, suite)
}
