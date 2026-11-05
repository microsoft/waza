package releasepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func assurancePolicyFixture(t *testing.T) (*Policy, *AssuranceContract, []byte) {
	t.Helper()
	p := testPolicy(t, 1)
	p.Requirements.Assurance = true
	p = sealTestPolicy(t, p)
	c := AssuranceContract{Kind: AssuranceContractKind, Version: AssuranceVersion,
		Nonce: strings.Repeat("ab", 32), PolicyDigest: p.Digest, Arms: map[Arm]AssuranceArm{}}
	for _, arm := range []Arm{Baseline, Candidate} {
		c.Arms[arm] = AssuranceArm{Mode: "authored_finite_output", PlanDigest: p.Arms[arm].Digest,
			EvalSourceDigest: SourceDigest([]byte("eval")), ExecutableDigest: SourceDigest([]byte("executable")),
			LabelsDigest: SourceDigest([]byte("synthetic labels")), ResolvedConfigDigest: testDigest(t, "config"),
			Tasks: []AssuranceTask{{TaskID: "task-0", Digest: testDigest(t, "task")}},
			Checks: []AssuranceCheck{{TaskID: "task-0", RequirementID: "output",
				Check:       models.RequirementCheck{Scope: "task", Grader: "test-check"},
				Declaration: testDigest(t, "grader"), ValidationKey: "test-check"}},
			Inputs: []AssuranceInput{{Path: "case.json", Digest: SourceDigest([]byte("synthetic authored output"))}}}
	}
	data, err := SealJSON(c)
	require.NoError(t, err)
	decoded, err := DecodeAssuranceContract(data, p)
	require.NoError(t, err)
	return p, decoded, data
}

func TestAssuranceContractAdmission(t *testing.T) {
	p, _, data := assurancePolicyFixture(t)
	object := func(value any) map[string]any {
		m, ok := value.(map[string]any)
		require.True(t, ok, "synthetic fixture object required")
		return m
	}
	array := func(value any) []any {
		a, ok := value.([]any)
		require.True(t, ok, "synthetic fixture array required")
		return a
	}
	baseline := func(m map[string]any) map[string]any { return object(object(m["arms"])["baseline"]) }
	for name, mutate := range map[string]func(map[string]any){
		"paid_version":    func(m map[string]any) { m["version"] = "1.1" },
		"wrong_kind":      func(m map[string]any) { m["kind"] = "other" },
		"uppercase_nonce": func(m map[string]any) { m["nonce"] = strings.Repeat("AB", 32) },
		"short_nonce":     func(m map[string]any) { m["nonce"] = "ab" },
		"unknown":         func(m map[string]any) { m["unknown"] = true },
		"alias":           func(m map[string]any) { m["Kind"] = AssuranceContractKind },
		"null":            func(m map[string]any) { m["arms"] = nil },
		"wrong_type":      func(m map[string]any) { m["arms"] = []any{} },
		"missing":         func(m map[string]any) { delete(m, "nonce") },
		"arm_swap": func(m map[string]any) {
			arms := object(m["arms"])
			arms["other"] = arms["candidate"]
			delete(arms, "candidate")
		},
		"nested_alias": func(m map[string]any) {
			baseline(m)["Mode"] = "authored_finite_output"
		},
		"task_duplicate": func(m map[string]any) {
			a := baseline(m)
			tasks := array(a["tasks"])
			a["tasks"] = append(tasks, tasks[0])
		},
		"mapping_duplicate": func(m map[string]any) {
			a := baseline(m)
			checks := array(a["checks"])
			a["checks"] = append(checks, checks[0])
		},
		"mapping_missing": func(m map[string]any) {
			baseline(m)["checks"] = []any{}
		},
		"unsupported_scope": func(m map[string]any) {
			a := baseline(m)
			object(object(array(a["checks"])[0])["check"])["scope"] = "checkpoint"
		},
		"digest_domain": func(m map[string]any) {
			object(baseline(m)["eval_source_digest"])["encoding"] = "json-v1"
		},
		"path_escape": func(m map[string]any) {
			a := baseline(m)
			object(array(a["inputs"])[0])["path"] = "../case.json"
		},
		"path_alias": func(m map[string]any) {
			a := baseline(m)
			object(array(a["inputs"])[0])["path"] = "./case.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := objectJSON(data)
			require.NoError(t, err)
			mutate(m)
			tampered, err := SealJSON(m)
			require.NoError(t, err)
			_, err = DecodeAssuranceContract(tampered, p)
			require.Error(t, err)
		})
	}
	duplicate := strings.Replace(string(data), `"nonce":`, `"nonce":"`+strings.Repeat("ab", 32)+`","nonce":`, 1)
	_, err := DecodeAssuranceContract([]byte(duplicate), p)
	require.Error(t, err)
	p.Requirements.Assurance = false
	_, err = DecodeAssuranceContract(data, p)
	require.Error(t, err)
}

func TestAssuranceOutputPresence(t *testing.T) {
	empty, text := "", "hello"
	for _, tc := range []struct {
		output AssuranceOutput
		valid  bool
	}{{AssuranceOutput{Availability: "available", Value: &empty}, true},
		{AssuranceOutput{Availability: "available", Value: &text}, true},
		{AssuranceOutput{Availability: "available"}, false},
		{AssuranceOutput{Availability: "available", Value: &empty, Reason: "missing"}, false},
		{AssuranceOutput{Availability: "unavailable", Reason: "No response returned."}, true},
		{AssuranceOutput{Availability: "unavailable", Value: &empty, Reason: "missing"}, false},
		{AssuranceOutput{Availability: "unavailable"}, false},
		{AssuranceOutput{Availability: "unknown"}, false}} {
		require.Equal(t, tc.valid, validateAssuranceOutput(tc.output) == nil, "%+v", tc.output)
	}
}

func TestAssuranceTapeSyncFailureDoesNotPublishCoreTerminal(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("durable collection unsupported")
	}
	p, c, data := assurancePolicyFixture(t)
	bound := &assuranceCollection{contract: c, data: data}
	collector := testCollector(p)
	collector.BeforePublish = func(context.Context) error { return nil }
	collector.Initialize = func(context.Context) error {
		bound.sync = func() error { return errors.New("synthetic fsync failure") }
		return nil
	}
	attempt := collector.Attempt
	collector.Attempt = func(ctx context.Context, key AttemptKey) (AttemptObservation, error) {
		row, err := attempt(ctx, key)
		empty := ""
		row.Output = &AssuranceOutput{Availability: "available", Value: &empty}
		return row, err
	}
	dir := filepath.Join(t.TempDir(), "collection")
	err := collect(t.Context(), testBytes(t, p), dir, collector, bound)
	require.ErrorContains(t, err, "synthetic fsync failure")
	core, err := os.ReadFile(filepath.Join(dir, "journal.ndjson"))
	require.NoError(t, err)
	require.Contains(t, string(core), `"attempt_start"`)
	require.NotContains(t, string(core), `"attempt_terminal"`)
	payload, err := os.ReadFile(filepath.Join(dir, "assurance-rows.ndjson"))
	require.NoError(t, err)
	require.Contains(t, string(payload), `"run"`)
	require.Contains(t, string(payload), `"validations"`)
	require.NoFileExists(t, filepath.Join(dir, "assurance-ledger.json"))
	require.NoFileExists(t, filepath.Join(dir, "journal.json"))
	decision, err := ReadDecision(dir)
	require.Error(t, err)
	require.False(t, decision.Accepted)
}

func TestAssuranceLedgerTamperAndOperationalAccounting(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("durable collection unsupported")
	}
	p, c, contract := assurancePolicyFixture(t)
	collector := testCollector(p)
	collector.BeforePublish = func(context.Context) error { return nil }
	attempt := collector.Attempt
	collector.Attempt = func(ctx context.Context, key AttemptKey) (AttemptObservation, error) {
		row, err := attempt(ctx, key)
		if err != nil {
			return row, err
		}
		if key.Arm == Candidate {
			row.Summary.Status, row.Summary.Category, row.Summary.Checks = "incomplete", "operational", []CheckSummary{}
			row, err = syntheticObservation(row.Summary)
			row.Output = &AssuranceOutput{Availability: "unavailable", Reason: "Synthetic execution has no response."}
		} else {
			empty := ""
			row.Output = &AssuranceOutput{Availability: "available", Value: &empty}
		}
		return row, err
	}
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, CollectAssured(t.Context(), testBytes(t, p), contract, dir, collector))
	ledger, results, err := ReadAssuranceLedger(dir, p, c)
	require.NoError(t, err)
	require.Len(t, ledger.Rows, 4)
	require.Len(t, results[Candidate].Rows, 2)
	unavailable := 0
	for _, row := range ledger.Rows {
		if row.Output.Availability == "unavailable" {
			unavailable++
		}
	}
	require.Equal(t, 2, unavailable)
	for _, artifact := range []string{"assurance-contract.json", "assurance.ndjson", "assurance-rows.ndjson", "assurance-ledger.json",
		"baseline.results.json", "candidate.results.json", "journal.json"} {
		t.Run(artifact, func(t *testing.T) {
			path := filepath.Join(dir, artifact)
			original, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(original, '\n'), 0600))
			_, _, err = ReadAssuranceLedger(dir, p, c)
			if artifact == "assurance-contract.json" || artifact == "journal.json" || artifact == "assurance-ledger.json" {
				// JSON-v1 identities intentionally ignore harmless whitespace.
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.NoError(t, os.WriteFile(path, original, 0600))
			require.NoError(t, os.Remove(path))
			_, _, err = ReadAssuranceLedger(dir, p, c)
			require.Error(t, err)
			require.NoError(t, os.WriteFile(path, original, 0600))
		})
	}
	payloadPath := filepath.Join(dir, "assurance-rows.ndjson")
	payload, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSuffix(string(payload), "\n"), "\n")
	for name, corrupt := range map[string]string{
		"missing":   strings.Join(lines[:len(lines)-1], "\n") + "\n",
		"extra":     string(payload) + lines[0] + "\n",
		"duplicate": lines[0] + "\n" + lines[0] + "\n" + strings.Join(lines[2:], "\n") + "\n",
		"reordered": lines[1] + "\n" + lines[0] + "\n" + strings.Join(lines[2:], "\n") + "\n",
		"torn":      strings.TrimSuffix(string(payload), "\n"),
		"output":    strings.Replace(string(payload), `"final_output":""`, `"final_output":"tampered"`, 1),
		"details":   strings.Replace(string(payload), `"score":1`, `"score":0`, 1),
		"origin":    strings.Replace(string(payload), `"attempt_count":1`, `"attempt_count":2`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, string(payload), corrupt)
			require.NoError(t, os.WriteFile(payloadPath, []byte(corrupt), 0600))
			_, _, err := ReadAssuranceLedger(dir, p, c)
			require.Error(t, err)
			require.NoError(t, os.WriteFile(payloadPath, payload, 0600))
		})
	}
	raw, err := os.ReadFile(filepath.Join(dir, "assurance-ledger.json"))
	require.NoError(t, err)
	var decoded AssuranceLedger
	require.NoError(t, json.Unmarshal(raw, &decoded))
	decoded.Rows[0], decoded.Rows[1] = decoded.Rows[1], decoded.Rows[0]
	changed, err := SealJSON(decoded)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assurance-ledger.json"), changed, 0600))
	_, _, err = ReadAssuranceLedger(dir, p, c)
	require.Error(t, err)
}

func TestAssurancePersistenceFailureSeams(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("durable collection unsupported")
	}
	for _, seam := range []string{"payload_sync", "after_assurance_sync", "before_final_publication"} {
		t.Run(seam, func(t *testing.T) {
			p, c, data := assurancePolicyFixture(t)
			bound := &assuranceCollection{contract: c, data: data}
			collector := testCollector(p)
			collector.BeforePublish = func(context.Context) error {
				if seam == "before_final_publication" {
					return errors.New("synthetic prepublication crash")
				}
				return nil
			}
			collector.Initialize = func(context.Context) error {
				switch seam {
				case "payload_sync":
					bound.rawSync = func() error { return errors.New("synthetic full-row fsync failure") }
				case "after_assurance_sync":
					sync := bound.sync
					bound.sync = func() error {
						require.NoError(t, sync())
						panic("synthetic process failure before core terminal")
					}
				}
				return nil
			}
			attempt := collector.Attempt
			collector.Attempt = func(ctx context.Context, key AttemptKey) (AttemptObservation, error) {
				row, err := attempt(ctx, key)
				empty := ""
				row.Output = &AssuranceOutput{Availability: "available", Value: &empty}
				return row, err
			}
			dir := filepath.Join(t.TempDir(), "collection")
			if seam == "after_assurance_sync" {
				require.Panics(t, func() { require.NoError(t, collect(t.Context(), testBytes(t, p), dir, collector, bound)) })
			} else {
				require.Error(t, collect(t.Context(), testBytes(t, p), dir, collector, bound))
			}
			core, err := os.ReadFile(filepath.Join(dir, "journal.ndjson"))
			require.NoError(t, err)
			if seam == "before_final_publication" {
				require.Contains(t, string(core), `"attempt_terminal"`)
			} else {
				require.NotContains(t, string(core), `"attempt_terminal"`)
			}
			payload, err := os.ReadFile(filepath.Join(dir, "assurance-rows.ndjson"))
			require.NoError(t, err)
			require.Contains(t, string(payload), `"validations"`)
			require.NoFileExists(t, filepath.Join(dir, "assurance-ledger.json"))
			require.NoFileExists(t, filepath.Join(dir, "journal.json"))
		})
	}
}
