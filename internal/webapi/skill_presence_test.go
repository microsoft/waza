package webapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordedSkillPresenceFromDiskToAPI(t *testing.T) {
	for _, configured := range []bool{false, true} {
		backend := "file-store"
		if configured {
			backend = "configured-local-adapter"
		}
		for _, version := range []string{"", "1.0", "1.4", "1.5"} {
			for _, tc := range []struct {
				name, field, value string
				present            bool
			}{
				{name: "absent"},
				{name: "null", field: `,"skill":null`},
				{name: "blank", field: `,"skill":""`, present: true},
				{name: "named", field: `,"skill":"recorded-skill"`, value: "recorded-skill", present: true},
				{name: "authored-unavailable", field: `,"skill":"Unavailable"`, value: "Unavailable", present: true},
			} {
				t.Run(backend+"/"+version+"/"+tc.name, func(t *testing.T) {
					header := ""
					if version != "" {
						header = `,"schemaVersion":"` + version + `"`
					}
					raw := []byte(`{"eval_id":"recorded","eval_name":"actual-eval","summary":{"total_tests":1,"statistics":{"pass_hat_k":[0.25]}},"tasks":[{"test_id":"task","display_name":"Recorded task","stats":{"avg_score":0.5}}],"baseline_outcome":{"schemaVersion":"1.4"}` + header + tc.field + `}`)
					dir := t.TempDir()
					path := filepath.Join(dir, "recorded.json")
					require.NoError(t, os.WriteFile(path, raw, 0o600))
					mux := labRefreshMux(t, dir, configured)
					for _, endpoint := range []string{"/api/v1/lab/runs", "/api/runs", "/api/runs/recorded"} {
						response := labRefreshRequest(t, mux, endpoint, http.StatusOK)
						var fields map[string]json.RawMessage
						if endpoint == "/api/runs/recorded" {
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &fields))
							var tasks []TaskResult
							require.NoError(t, json.Unmarshal(fields["tasks"], &tasks))
							require.Len(t, tasks, 1, "detail serialization must retain embedded fields")
							require.Equal(t, "task", tasks[0].ID)
							require.Nil(t, tasks[0].WeightedScore)
							var statistics ReliabilitySummaryResponse
							require.NoError(t, json.Unmarshal(fields["statistics"], &statistics))
							require.Equal(t, []float64{0.25}, statistics.PassHatK)
						} else {
							var list []map[string]json.RawMessage
							require.NoError(t, json.Unmarshal(response.Body.Bytes(), &list))
							require.Len(t, list, 1)
							fields = list[0]
						}
						require.Equal(t, `"actual-eval"`, string(fields["spec"]))
						require.Equal(t, `"recorded"`, string(fields["id"]))
						require.Nil(t, fields["weightedScore"], "skill presence must not fabricate scores")
						require.Equal(t, tc.present, fields["skill"] != nil, endpoint)
						if tc.present {
							var skill string
							require.NoError(t, json.Unmarshal(fields["skill"], &skill))
							require.Equal(t, tc.value, skill, endpoint)
						}
					}
					after, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, raw, after, "reading either API must not rewrite historical artifacts")
				})
			}
		}
	}
}
