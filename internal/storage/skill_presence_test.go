package storage

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestStoredRecordedSkillPresence(t *testing.T) {
	for _, tc := range []struct {
		name, field, value string
		present            bool
	}{
		{name: "absent"},
		{name: "null", field: `,"skill":null`},
		{name: "blank", field: `,"skill":""`, present: true},
		{name: "named", field: `,"skill":"recorded-skill"`, value: "recorded-skill", present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"eval_id":"recorded","eval_name":"not-a-skill","timestamp":"2026-10-14T00:00:00Z","summary":{},"tasks":[]` + tc.field + `}`)
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "recorded.json"), raw, 0o600))
			local := NewLocalStore(dir)
			list, err := local.List(t.Context(), ListOptions{})
			require.NoError(t, err)
			require.Len(t, list, 1)
			require.Equal(t, tc.value, list[0].Skill)
			require.Equal(t, tc.present, list[0].HasSkill())
			assertPresence := func(data []byte) {
				t.Helper()
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(data, &fields))
				require.Equal(t, tc.present, fields["skill"] != nil)
				if tc.present {
					var value string
					require.NoError(t, json.Unmarshal(fields["skill"], &value))
					require.Equal(t, tc.value, value)
				}
			}
			data, err := json.Marshal(list[0])
			require.NoError(t, err)
			assertPresence(data)
			outcome, err := local.Download(t.Context(), "recorded")
			require.NoError(t, err)
			require.Equal(t, tc.present, outcome.HasSkillTested())
			require.NoError(t, local.Upload(t.Context(), outcome))
			rewritten, err := os.ReadFile(filepath.Join(dir, "recorded.json"))
			require.NoError(t, err)
			assertPresence(rewritten)
			require.NoError(t, local.Reload())
			reloaded, err := local.Download(t.Context(), "recorded")
			require.NoError(t, err)
			require.Equal(t, outcome, reloaded)

			opts := azureBlobClientOptions()
			requests := 0
			opts.Transport = azureBlobTestTransport(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Method == http.MethodPut {
					body, err := io.ReadAll(req.Body)
					require.NoError(t, err)
					assertPresence(body)
					hasSkill := false
					for key, values := range req.Header {
						if strings.EqualFold(key, "x-ms-meta-skill") {
							hasSkill = true
							require.Equal(t, []string{tc.value}, values)
						}
					}
					require.Equal(t, tc.present, hasSkill)
					return &http.Response{
						StatusCode: http.StatusCreated, Header: make(http.Header),
						Body: io.NopCloser(strings.NewReader("")), Request: req,
					}, nil
				}
				require.Equal(t, http.MethodGet, req.Method)
				return &http.Response{
					StatusCode: http.StatusOK, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(string(raw))), Request: req,
				}, nil
			})
			client, err := azblob.NewClientWithNoCredential("https://example.blob.core.windows.net/", opts)
			require.NoError(t, err)
			remote := &AzureBlobStore{client: client, containerName: "results"}
			require.NoError(t, remote.Upload(t.Context(), outcome))
			downloaded, err := remote.DownloadListedResult(t.Context(), ResultSummary{BlobPath: "recorded.json"})
			require.NoError(t, err)
			require.Equal(t, 2, requests, "both operations use only the in-memory HTTP transport")
			require.Equal(t, tc.present, downloaded.HasSkillTested())
			summary := remote.outcomeToResultSummary(downloaded)
			require.Equal(t, tc.present, summary.HasSkill())
			data, err = json.Marshal(summary)
			require.NoError(t, err)
			assertPresence(data)
		})
	}
}

func TestBlobMetadataSkillPresence(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     *string
		present   bool
	}{
		{name: "absent"},
		{name: "nil", key: "skill"},
		{name: "blank", key: "skill", value: stringPtr(""), present: true},
		{name: "named", key: "skill", value: stringPtr("recorded-skill"), present: true},
		{name: "case-insensitive-blank", key: "Skill", value: stringPtr(""), present: true},
		{name: "case-insensitive-named", key: "Skill", value: stringPtr("recorded-skill"), present: true},
		{name: "uppercase-named", key: "SKILL", value: stringPtr("upper-skill"), present: true},
		{name: "mixed-case-named", key: "sKiLl", value: stringPtr("mixed-skill"), present: true},
		{name: "case-insensitive-nil", key: "Skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]*string{
				"runid": stringPtr("recorded"), "timestamp": stringPtr("2026-10-14T00:00:00Z"),
			}
			if tc.key != "" {
				metadata[tc.key] = tc.value
			}
			summary, err := stubAzureBlobStore().blobToResultSummary(&container.BlobItem{
				Name: stringPtr("recorded.json"), Metadata: metadata,
			})
			require.NoError(t, err)
			require.Equal(t, tc.present, summary.HasSkill())
			data, err := json.Marshal(summary)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			require.Equal(t, tc.present, fields["skill"] != nil)
			if tc.value != nil {
				var skill string
				require.NoError(t, json.Unmarshal(fields["skill"], &skill))
				require.Equal(t, *tc.value, skill)
			}
		})
	}
	outcome := models.EvaluationOutcome{}
	require.True(t, outcomeToResultSummary(&outcome, "").HasSkill(), "fresh writer blanks retain existing semantics")
}

func TestBlobMetadataSkillEntryPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		entries  map[string]*string
		want     string
		recorded bool
	}{
		{
			name:    "canonical-named",
			entries: map[string]*string{"skill": stringPtr("canonical"), "SKILL": stringPtr("other")},
			want:    "canonical", recorded: true,
		},
		{
			name:     "canonical-blank",
			entries:  map[string]*string{"skill": stringPtr(""), "Skill": stringPtr("other")},
			recorded: true,
		},
		{
			name:    "canonical-nil-unavailable",
			entries: map[string]*string{"skill": nil, "Skill": stringPtr("other")},
		},
		{
			name:    "variants-bytewise-first",
			entries: map[string]*string{"sKiLl": stringPtr("mixed"), "Skill": stringPtr("title"), "SKILL": stringPtr("upper")},
			want:    "upper", recorded: true,
		},
		{
			name:     "selected-variant-blank",
			entries:  map[string]*string{"SKILL": stringPtr(""), "Skill": stringPtr("other")},
			recorded: true,
		},
		{
			name:    "selected-variant-nil-unavailable",
			entries: map[string]*string{"SKILL": nil, "Skill": stringPtr("other")},
		},
		{
			name:    "unrelated-key-ignored",
			entries: map[string]*string{"skills": stringPtr("unrelated"), "Skill": stringPtr("actual")},
			want:    "actual", recorded: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := map[string]*string{
				"runid": stringPtr("recorded"), "timestamp": stringPtr("2026-10-14T00:00:00Z"),
			}
			for key, value := range tc.entries {
				metadata[key] = value
			}
			for range 32 {
				summary, err := stubAzureBlobStore().blobToResultSummary(&container.BlobItem{
					Name: stringPtr("recorded.json"), Metadata: metadata,
				})
				require.NoError(t, err)
				require.Equal(t, tc.want, summary.Skill)
				require.Equal(t, tc.recorded, summary.HasSkill())
				data, err := json.Marshal(summary)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(data, &fields))
				require.Equal(t, tc.recorded, fields["skill"] != nil)
				if tc.recorded {
					var skill string
					require.NoError(t, json.Unmarshal(fields["skill"], &skill))
					require.Equal(t, tc.want, skill)
				}
			}
		})
	}
}
