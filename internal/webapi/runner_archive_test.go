package webapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestRunnerArchiveBoundsOrderingAndPagination(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	result.Observation.FinalOutput = strings.Repeat("x", 9<<20)
	artifact, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, runnercontract.ValidateResult(&result))
	require.Greater(t, len(artifact)*2, runnercontract.MaxResultBytes)
	for _, name := range []string{"z.runner.json", "a.runner.json"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), artifact, 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".runner"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".runner", "bad.runner.json"), []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oversize.runner.json"),
		[]byte(strings.Repeat(" ", runnercontract.MaxResultBytes+1)), 0o600))
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, dir)
	var names, outcomes []string
	cursor := ""
	pages := 0
	for {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
			"/api/v1/lab/runner-results?cursor="+url.QueryEscape(cursor), nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String()[:min(200, w.Body.Len())])
		require.LessOrEqual(t, w.Body.Len(), maxRunnerPageBytes)
		var page runnerArchivePage
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
		require.Equal(t, runnerArchiveVersion, page.SchemaVersion)
		for _, entry := range page.Results {
			names = append(names, entry.Artifact)
			if len(entry.Result) > 0 {
				recorded, err := runnercontract.ParseResult(entry.Result)
				require.NoError(t, err)
				outcomes = append(outcomes, string(recorded.Outcome))
				require.Empty(t, entry.Error)
			} else {
				require.NotEmpty(t, entry.Error)
			}
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		require.NotEqual(t, cursor, *page.NextCursor)
		cursor = *page.NextCursor
		require.Less(t, pages, 5)
	}
	require.Equal(t, []string{".runner/bad.runner.json", "a.runner.json", "oversize.runner.json", "z.runner.json"}, names)
	require.Equal(t, []string{"success", "success"}, outcomes)
	require.Equal(t, 2, pages, "aggregate byte bound must paginate without hiding either valid result")
	for _, limit := range []int{1, 2, 100} {
		page, err := readRunnerPage(t.Context(), dir, "", limit)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Results), limit)
		require.Equal(t, names[0], page.Results[0].Artifact)
		require.NotNil(t, page.NextCursor)
	}
}

func TestRunnerArchiveExactMaximumItemFitsPage(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	result.Observation.FinalOutput = ""
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	result.Observation.FinalOutput = strings.Repeat("x", runnercontract.MaxResultBytes-len(encoded)-1)
	require.NoError(t, runnercontract.ValidateResult(&result))
	encoded, err = json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "max.runner.json"), append(encoded, '\n'), 0o600))
	page, err := readRunnerPage(t.Context(), dir, "", 20)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Empty(t, page.Results[0].Error)
	wire, err := json.Marshal(page)
	require.NoError(t, err)
	require.Greater(t, len(wire), runnercontract.MaxResultBytes, "page needs framing allowance beyond item bound")
	require.Less(t, len(wire), maxRunnerPageBytes)
}

func TestRunnerArchiveRejectsInvalidContinuationAndLimits(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRunnerRoutes(mux, t.TempDir())
	unsupported := base64.RawURLEncoding.EncodeToString([]byte(`{"version":"2.0","after":"eA"}`))
	for _, query := range []string{"limit=0", "limit=101", "limit=x", "cursor=x",
		"cursor=" + unsupported, "cursor=" + archiveCursor(""), "cursor=" + strings.Repeat("a", 1025)} {
		t.Run(fmt.Sprint(query[:min(len(query), 40)]), func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/lab/runner-results?"+query, nil))
			require.Equal(t, http.StatusBadRequest, w.Code)
		})
	}
}

func TestRunnerArchivePublishedSchema(t *testing.T) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-results-1.1.schema.json": schemas.RunnerResultsSchemaJSON,
		"runner-result-1.0.schema.json":  schemas.RunnerResultSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		require.NoError(t, err)
		require.NoError(t, compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc))
	}
	schema, err := compiler.Compile("https://microsoft.github.io/waza/schemas/runner-results-1.1.schema.json")
	require.NoError(t, err)
	dir := t.TempDir()
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	require.NoError(t, err)
	result := runnercontract.NewService().Run(t.Context(), data)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.runner.json"), encoded, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.runner.json"), []byte("{"), 0o600))
	page, err := readRunnerPage(t.Context(), dir, "", 20)
	require.NoError(t, err)
	wire, err := json.Marshal(page)
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(wire)))
	require.NoError(t, err)
	require.NoError(t, schema.Validate(doc))
	var invalid map[string]any
	require.NoError(t, json.Unmarshal(wire, &invalid))
	invalid["next_cursor"] = 42
	require.Error(t, schema.Validate(invalid))
}
