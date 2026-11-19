package storage

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func nestedJSONRecord(t *testing.T, value any) map[string]any {
	t.Helper()
	items, ok := value.([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	record, ok := items[0].(map[string]any)
	require.True(t, ok)
	return record
}

func TestLocalStoreReloadIsolatesCallerMutation(t *testing.T) {
	for _, dashboard := range []bool{false, true} {
		for _, presence := range []string{"omitted", "null", "zero"} {
			t.Run(strconv.FormatBool(dashboard)+"/"+presence, func(t *testing.T) {
				score := ""
				skill := ""
				switch presence {
				case "null":
					score = `"weighted_score":null,`
					skill = `"skill":null,`
				case "zero":
					score = `"weighted_score":0,`
					skill = `"skill":"",`
				}
				statsScore := ""
				if presence != "omitted" {
					value := "null"
					if presence == "zero" {
						value = "0"
					}
					statsScore = `"avg_weighted_score":` + value + `,`
				}
				data := []byte(`{` + skill + `"eval_id":"cached","summary":{` + score + `"statistics":{"is_significant":false}},
					"metadata":{"nested":[{"value":"recorded","null":null,"empty":[]}],"zero":0,"false":false},
					"tasks":[{"test_id":"task","stats":{` + statsScore + `"is_significant":false},
					"runs":[{"validations":{"grader":{"details":{"nested":[{"value":"recorded"}]}}},
					"transcript":[{"type":"tool.execution_start","tool_call_id":"call","tool_name":"bash","arguments":{"nested":[{"value":"recorded"}]}},
					{"type":"future.event","arguments":{"nested":[{"value":"recorded"}]}}],
					"session_digest":{"usage":{"ai_credits":0,"model_metrics":{"model":{"ai_credits":0}}}}}]}],
					"baseline_outcome":{"eval_id":"baseline","summary":{}}
				}`)
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "cached.json"), data, 0o600))
				store := NewLocalStore(dir)
				if dashboard {
					store = NewDashboardLocalStore(dir)
				}
				before, err := store.Download(t.Context(), "cached")
				require.NoError(t, err)
				require.Equal(t, "1.4", before.SchemaVersion, "legacy reader default must survive")
				require.Equal(t, presence == "zero", before.HasSkillTested())
				require.Equal(t, presence == "zero", before.Digest.HasWeightedScore())
				require.Equal(t, presence == "zero", before.TestOutcomes[0].Stats.HasAvgWeightedScore())
				wire, err := json.Marshal(before)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(wire, &fields))
				var summary, stats map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(fields["summary"], &summary))
				require.JSONEq(t, `{"is_significant":false}`, string(summary["statistics"]))
				statsWire, err := json.Marshal(before.TestOutcomes[0].Stats)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(statsWire, &stats))
				require.JSONEq(t, `false`, string(stats["is_significant"]))
				if presence == "zero" {
					require.JSONEq(t, `""`, string(fields["skill"]))
					require.JSONEq(t, `0`, string(summary["weighted_score"]))
					require.JSONEq(t, `0`, string(stats["avg_weighted_score"]))
				} else {
					require.NotContains(t, fields, "skill")
					require.NotContains(t, summary, "weighted_score")
					require.NotContains(t, stats, "avg_weighted_score")
				}
				require.JSONEq(t, `{"nested":[{"value":"recorded","null":null,"empty":[]}],"zero":0,"false":false}`, string(fields["metadata"]))

				before.SkillTested = "caller-mutated"
				nestedJSONRecord(t, before.Metadata["nested"])["value"] = "caller-mutated"
				run := &before.TestOutcomes[0].Runs[0]
				nestedJSONRecord(t, run.Validations["grader"].Details["nested"])["value"] = "caller-mutated"
				start, ok := run.Transcript[0].Data.(*copilot.ToolExecutionStartData)
				require.True(t, ok, "exercise the typed SDK transcript payload")
				args, ok := start.Arguments.(map[string]any)
				require.True(t, ok)
				nestedJSONRecord(t, args["nested"])["value"] = "caller-mutated"
				raw, ok := run.Transcript[1].Data.(*copilot.RawSessionEventData)
				require.True(t, ok)
				recordedRaw := bytes.Clone(raw.Raw)
				raw.Raw[0] = '!'
				*run.SessionDigest.Usage.AICredits = 42
				*run.SessionDigest.Usage.ModelMetrics["model"].AICredits = 42
				*before.TestOutcomes[0].Stats.IsSignificant = true //nolint:staticcheck // Exercise historical recorded-false pointer ownership.
				before.BaselineOutcome.SkillTested = "caller-mutated"

				for range 2 {
					require.NoError(t, store.Reload())
					after, err := store.Download(t.Context(), "cached")
					require.NoError(t, err)
					afterWire, err := json.Marshal(after)
					require.NoError(t, err)
					require.JSONEq(t, string(wire), string(afterWire), "refresh must recover the recorded artifact, including private presence flags and nested values")
					afterRaw, ok := after.TestOutcomes[0].Runs[0].Transcript[1].Data.(*copilot.RawSessionEventData)
					require.True(t, ok)
					require.Equal(t, recordedRaw, []byte(afterRaw.Raw), "private cache must retain unprojected raw transcript data too")
					require.NotSame(t, before, after)
				}
			})
		}
	}
}

func TestDashboardResultSnapshotDoesNotOwnRetainedCache(t *testing.T) {
	dir := t.TempDir()
	data, err := json.Marshal(makeOutcome("cached", "recorded", "mock-model", 1, 1))
	require.NoError(t, err)
	path := filepath.Join(dir, "cached.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	cache := NewDashboardResultCache()
	results, files, err := ReadLocalResultsForDashboard(dir, nil, cache)
	require.NoError(t, err)
	results["cached"].SkillTested = "caller-mutated"
	delete(results, "cached")
	results, _, err = ReadLocalResultsForDashboard(dir, files, cache)
	require.NoError(t, err)
	require.Equal(t, "recorded", results["cached"].SkillTested)
	require.NoError(t, os.Remove(path))
	results, _, err = ReadLocalResultsForDashboard(dir, files, cache)
	require.NoError(t, err)
	require.Empty(t, results)
	require.Empty(t, cache.entries, "successful refresh bounds retention to currently admitted files")
}

func TestDashboardResultCacheConcurrentSnapshots(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"eval_id":"cached","metadata":{"nested":[{"value":"recorded"}]}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cached.json"), data, 0o600))
	cache := NewDashboardResultCache()
	_, files, err := ReadLocalResultsForDashboard(dir, nil, cache)
	require.NoError(t, err)
	for i := range 8 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			for range 3 {
				results, _, err := ReadLocalResultsForDashboard(dir, files, cache)
				require.NoError(t, err)
				nested := nestedJSONRecord(t, results["cached"].Metadata["nested"])
				require.Equal(t, "recorded", nested["value"])
				nested["value"] = "caller-mutated"
			}
		})
	}
}

func TestDashboardResultCacheNilUsesOrdinaryReader(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"eval_id":"cached","skill":"recorded"}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "cached.json"), data, 0o600))
	results, files, err := ReadLocalResultsForDashboard(dir, nil, nil)
	require.NoError(t, err)
	results["cached"].SkillTested = "caller-mutated"
	results, _, err = ReadLocalResultsForDashboard(dir, files, nil)
	require.NoError(t, err)
	require.Equal(t, "recorded", results["cached"].SkillTested)
	results, files, err = ReadLocalResultsForDashboard("", nil, NewDashboardResultCache())
	require.NoError(t, err)
	require.Empty(t, results)
	require.Empty(t, files)
}

func TestLocalStoreMissingEmptyDirectoryRemainsEmpty(t *testing.T) {
	store := NewLocalStore(filepath.Join(t.TempDir(), "not-created"))
	for range 3 {
		list, err := store.List(t.Context(), ListOptions{})
		require.NoError(t, err)
		require.Empty(t, list)
		require.NoError(t, store.Reload())
	}
}

func TestReadLocalResultsWalkErrorsRespectAdmission(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "known", "cached.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	data, err := json.Marshal(makeOutcome("cached", "skill", "mock", 1, 1))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(file, data, 0o600))
	info, err := os.Stat(file)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, path string
		fail       bool
	}{
		{"root", dir, true},
		{"known-file", file, true},
		{"known-subtree", filepath.Dir(file), true},
		{"new-file", filepath.Join(dir, "new.json"), false},
		{"unrelated-subtree", filepath.Join(dir, "unrelated"), false},
		{"similar-prefix-not-known", filepath.Join(dir, "known-other"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			walk := func(_ string, visit fs.WalkDirFunc) error {
				if err := visit(file, fs.FileInfoToDirEntry(info), nil); err != nil {
					return err
				}
				return visit(tc.path, nil, &os.PathError{Op: "readdir", Path: tc.path, Err: fs.ErrPermission})
			}
			results, files, err := readLocalResults(dir, map[string]struct{}{file: {}}, walk)
			if tc.fail {
				require.ErrorIs(t, err, fs.ErrPermission)
				require.ErrorContains(t, err, tc.path)
				require.Nil(t, results)
				require.Nil(t, files)
			} else {
				require.NoError(t, err)
				require.Contains(t, results, "cached")
				require.Contains(t, files, file)
			}
		})
	}
}

func TestReadLocalResultReadFailuresRespectAdmission(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, name := range []string{"missing-file", "directory"} {
			t.Run(name+"/"+map[bool]string{false: "new", true: "known"}[known], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "result.json")
				if name == "directory" {
					require.NoError(t, os.Mkdir(path, 0o700))
				}
				outcome, err := readLocalResult(path, known)
				require.Nil(t, outcome)
				if known {
					require.ErrorContains(t, err, path)
				} else {
					require.NoError(t, err, "unadmitted unreadable files remain skippable")
				}
			})
		}
	}
}

func TestLocalStoreReloadDirectoryFailurePreservesCache(t *testing.T) {
	dir := t.TempDir()
	store := NewLocalStore(dir)
	outcome := makeOutcome("cached", "skill", "mock-model", 1, 1)
	require.NoError(t, store.Upload(t.Context(), outcome))
	before, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	for name, path := range map[string]string{
		"missing": filepath.Join(t.TempDir(), "missing"),
		"file":    blocker,
		"invalid": filepath.Join(blocker, "results"),
	} {
		t.Run(name, func(t *testing.T) {
			store.dir = path
			require.Error(t, store.Reload())
			after, err := store.Download(t.Context(), "cached")
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
	store.dir = dir
	require.NoError(t, store.Reload())
	after, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestDashboardLocalStoreDetectsSameSizeChangeWithRestoredMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.json")
	before := makeOutcome("cached", "alpha", "mock-model", 1, 1)
	after := makeOutcome("cached", "bravo", "mock-model", 1, 1)
	beforeData, err := json.Marshal(before)
	require.NoError(t, err)
	afterData, err := json.Marshal(after)
	require.NoError(t, err)
	require.Len(t, afterData, len(beforeData))

	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(path, beforeData, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	store := NewDashboardLocalStore(dir)
	loadedBefore, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, "alpha", loadedBefore.SkillTested)
	require.NoError(t, store.Reload())
	unchanged, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.NotSame(t, loadedBefore, unchanged, "refresh snapshots must not expose the retained parsed outcome")
	require.Equal(t, loadedBefore, unchanged)

	require.NoError(t, os.WriteFile(path, afterData, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	require.NoError(t, store.Reload())
	loadedAfter, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Equal(t, "bravo", loadedAfter.SkillTested)
	require.NotSame(t, loadedBefore, loadedAfter)
}

func TestDashboardLocalStoreRejectsSameSizeInvalidChangeWithRestoredMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cached.json")
	outcome := makeOutcome("cached", "alpha", "mock-model", 1, 1)
	data, err := json.Marshal(outcome)
	require.NoError(t, err)
	mtime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))

	store := NewDashboardLocalStore(dir)
	before, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)

	invalid := bytes.Repeat([]byte{' '}, len(data))
	invalid[0] = '{'
	require.NoError(t, os.WriteFile(path, invalid, 0o600))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
	require.ErrorContains(t, store.Reload(), strconv.Quote(path))

	after, err := store.Download(t.Context(), "cached")
	require.NoError(t, err)
	require.Same(t, before, after, "failed refresh should preserve the prior snapshot")
}

func BenchmarkLocalResultRefresh300(b *testing.B) {
	dir := b.TempDir()
	for i := range 300 {
		outcome := makeOutcome("run-"+strconv.Itoa(i), "skill", "mock-model", 5, 10)
		data, err := json.Marshal(outcome)
		require.NoError(b, err)
		require.NoError(b, os.WriteFile(filepath.Join(dir, outcome.RunID+".json"), data, 0o600))
	}

	b.Run("uncached", func(b *testing.B) {
		_, files, err := ReadLocalResults(dir, nil)
		require.NoError(b, err)
		b.ResetTimer()
		for range b.N {
			_, files, err = ReadLocalResults(dir, files)
			require.NoError(b, err)
		}
	})
	b.Run("dashboard-cache", func(b *testing.B) {
		cache := NewDashboardResultCache()
		_, files, err := ReadLocalResultsForDashboard(dir, nil, cache)
		require.NoError(b, err)
		b.ResetTimer()
		for range b.N {
			_, files, err = ReadLocalResultsForDashboard(dir, files, cache)
			require.NoError(b, err)
		}
	})
}

func dashboardEvidenceResult(t *testing.T, unknown bool) []byte {
	t.Helper()
	manifest := `{"version":"1.0","sha256":"291a3cecd450615b3f14df209e47565f668f0dad298eb69d746a30398353db42",
		"origin":{"eval_id":"cached","task_id":"task","run_number":1,"attempt_count":1,"prior_attempts":"none"},
		"runtime":{"waza_version":"","requested_engine":"","requested_model":"","execution_mode":"unknown",
		"dependency_mode":"unknown","requested_policy":"unknown","verified_enforcement":"unknown","go_version":"go1.26.4",
		"platform":"darwin/arm64","sdk_version":"1.0","effective_model_version":null,"no_skills":false,"native_skill_control":"unknown"},
		"redaction":{"policy":"default","applied_rules":["rule"],"match_count":0,"limitations":["Source digests do not preserve contents."]},
		"artifacts":[{"id":"prompt","kind":"prompt","availability":"captured","completeness":"unknown","document":"snapshot","pointer":"/prompt",
		"content_digest":{"sha256":"15c53b41755b7dbbf631697798b043b1eb429674afb2580b605d468c7f8593b7","encoding":"json-v1"},
		"reason":"Collector completeness is not certified."}]`
	if unknown {
		manifest += `,"future_field":{"nested":"recorded"}`
	}
	manifest += `}`
	return []byte(`{"eval_id":"cached","tasks":[{"test_id":"task","runs":[{"run_number":1,"attempts":1,"evidence":` + manifest + `}]}]}`)
}

func TestDashboardLocalStoreClonesEvidenceManifest(t *testing.T) {
	for _, dashboard := range []bool{false, true} {
		for _, unknown := range []bool{false, true} {
			t.Run(strconv.FormatBool(dashboard)+"/unknown="+strconv.FormatBool(unknown), func(t *testing.T) {
				dir := t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(dir, "cached.json"), dashboardEvidenceResult(t, unknown), 0o600))
				store := NewLocalStore(dir)
				if dashboard {
					store = NewDashboardLocalStore(dir)
				}
				before, err := store.Download(t.Context(), "cached")
				require.NoError(t, err)
				evidence := before.TestOutcomes[0].Runs[0].Evidence
				require.NotNil(t, evidence)
				require.Equal(t, unknown, evidence.HasUnknownFields())
				wire, err := json.Marshal(before)
				require.NoError(t, err)

				evidence.Origin.EvalID = "caller-mutated"
				evidence.Redaction.AppliedRules[0] = "caller-mutated"
				evidence.Redaction.Limitations[0] = "caller-mutated"
				*evidence.Runtime.SDKVersion = "caller-mutated"
				*evidence.Runtime.NoSkills = true
				evidence.Artifacts[0].ContentDigest.SHA256 = "caller-mutated"
				if unknown {
					raw, err := json.Marshal(evidence)
					require.NoError(t, err)
					require.Contains(t, string(raw), "future_field", "unknown manifests marshal their retained raw bytes")
				}

				for range 2 {
					require.NoError(t, store.Reload())
					after, err := store.Download(t.Context(), "cached")
					require.NoError(t, err)
					afterEvidence := after.TestOutcomes[0].Runs[0].Evidence
					require.NotSame(t, evidence, afterEvidence)
					require.Equal(t, unknown, afterEvidence.HasUnknownFields())
					require.Equal(t, unknown, afterEvidence.HasInvalidSchema())
					require.False(t, afterEvidence.HasInvalidRequiredObjects())
					afterWire, err := json.Marshal(after)
					require.NoError(t, err)
					require.JSONEq(t, string(wire), string(afterWire))
				}
			})
		}
	}
}
