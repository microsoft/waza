package releasepolicy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReleaseRawVerdictsCannotDefault(t *testing.T) {
	directory := authoredRootCollection(t, testPolicy(t, 8), true)
	rawRows, err := os.ReadFile(filepath.Join(directory, "baseline.results.json"))
	require.NoError(t, err)
	rawBinding, err := os.ReadFile(filepath.Join(directory, "baseline.result-binding.json"))
	require.NoError(t, err)
	rows, err := DecodeResults(rawRows)
	require.NoError(t, err)
	binding, err := DecodeResult(rawBinding)
	require.NoError(t, err)
	summary := binding.Attempts[0]
	summary.Checks[0].Passed, summary.Checks[0].Score = false, "0"
	row := rows.Rows[0]
	verdict := row.Run.Validations[summary.Checks[0].Grader]
	verdict.Passed, verdict.Score = false, 0
	row.Run.Validations[summary.Checks[0].Grader] = verdict
	row.Run.Status, summary.Status = models.StatusFailed, string(models.StatusFailed)
	rows.Rows = []ActualRunRow{row}
	binding.Attempts = []AttemptSummary{summary}
	require.NoError(t, VerifyRunRow(summary, row))
	explicitRows, err := json.Marshal(rows)
	require.NoError(t, err)
	decodedRows, err := DecodeResults(explicitRows)
	require.NoError(t, err)
	require.NoError(t, VerifyRunRow(summary, decodedRows.Rows[0]))
	explicitBinding, err := json.Marshal(binding)
	require.NoError(t, err)
	decodedBinding, err := DecodeResult(explicitBinding)
	require.NoError(t, err)
	require.Equal(t, binding, decodedBinding)

	for _, document := range []string{"rows", "binding"} {
		for _, field := range []string{"passed", "score"} {
			for _, mutation := range []string{"missing", "null", "alias", "wrong type", "collision before", "collision after"} {
				t.Run(document+"/"+field+"/"+mutation, func(t *testing.T) {
					var raw []byte
					var err error
					if document == "rows" {
						raw, err = json.Marshal(rows)
					} else {
						raw, err = json.Marshal(binding)
					}
					require.NoError(t, err)
					object, err := objectJSON(raw)
					require.NoError(t, err)
					var check map[string]any
					if document == "rows" {
						actualRows, ok := object["rows"].([]any)
						require.True(t, ok)
						require.NotEmpty(t, actualRows)
						row, ok := actualRows[0].(map[string]any)
						require.True(t, ok)
						run, ok := row["run"].(map[string]any)
						require.True(t, ok)
						validations, ok := run["validations"].(map[string]any)
						require.True(t, ok)
						check, ok = validations[summary.Checks[0].Grader].(map[string]any)
						require.True(t, ok)
					} else {
						attempts, ok := object["attempts"].([]any)
						require.True(t, ok)
						require.NotEmpty(t, attempts)
						attempt, ok := attempts[0].(map[string]any)
						require.True(t, ok)
						checks, ok := attempt["checks"].([]any)
						require.True(t, ok)
						require.NotEmpty(t, checks)
						check, ok = checks[0].(map[string]any)
						require.True(t, ok)
					}
					switch mutation {
					case "missing":
						delete(check, field)
					case "null":
						check[field] = nil
					case "alias":
						check[string(field[0]-'a'+'A')+field[1:]] = check[field]
						delete(check, field)
					case "wrong type":
						check[field] = "not a verdict"
					}
					raw, err = json.Marshal(object)
					require.NoError(t, err)
					if mutation == "collision before" || mutation == "collision after" {
						canonical := `"` + field + `":false`
						alias := `"Passed":true`
						if field == "score" {
							canonical, alias = `"score":0`, `"Score":1`
						}
						replacement := alias + "," + canonical
						if mutation == "collision after" {
							replacement = canonical + "," + alias
						}
						require.Contains(t, string(raw), canonical)
						raw = bytes.Replace(raw, []byte(canonical), []byte(replacement), 1)
					}
					if document == "rows" {
						actual, err := DecodeResults(raw)
						require.Error(t, err, "malformed raw verdict must not become false/zero")
						require.Nil(t, actual)
					} else {
						actual, err := DecodeResult(raw)
						require.Error(t, err, "malformed raw verdict must not become false/zero")
						require.Nil(t, actual)
					}
				})
			}

			for _, document := range []string{"rows", "binding"} {
				for _, order := range []string{"before", "after"} {
					t.Run(document+"/container alias/"+order, func(t *testing.T) {
						raw, alias := explicitRows, `"Rows":[{}]`
						if document == "binding" {
							raw, alias = explicitBinding, `"Attempts":[{}]`
						}
						if order == "before" {
							raw = append([]byte("{"+alias+","), raw[1:]...)
						} else {
							raw = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(","+alias+"}")...)
						}
						if document == "rows" {
							actual, err := DecodeResults(raw)
							require.Error(t, err)
							require.Nil(t, actual)
						} else {
							actual, err := DecodeResult(raw)
							require.Error(t, err)
							require.Nil(t, actual)
						}
					})
				}
			}
		}
	}
}
