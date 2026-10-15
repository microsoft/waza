package controlledcomparison

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func assuredFixture(t *testing.T, n int, reviewed bool) (AssuranceSource, []byte) {
	return assuredFixtureForExecutable(t, n, reviewed, "")
}

func assuredFixtureForExecutable(t *testing.T, n int, reviewed bool, implementation string) (AssuranceSource, []byte) {
	t.Helper()
	source := fixture(t, "mock-label")
	names := []string{}
	for i := range n {
		name := fmt.Sprintf("task-%d.yaml", i)
		names = append(names, name)
		task := fmt.Sprintf("id: task-%d\nname: hello\ninputs:\n  prompt: hello\n  context:\n    fixture: fixtures\ngraders:\n  - name: ready\n    type: text\n    config:\n      contains: [hello]\nrequirements:\n  - id: output\n    category: outcome\n    description: Finite hello text\n    checks:\n      - scope: task\n        grader: ready\n", i)
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(source.EvalPath), name), []byte(task), 0600))
	}
	data, err := os.ReadFile(source.EvalPath)
	require.NoError(t, err)
	data = []byte(strings.ReplaceAll(string(data), "tasks: [task.yaml]", "tasks: ["+strings.Join(names, ", ")+"]"))
	require.NoError(t, os.WriteFile(source.EvalPath, data, 0600))
	d := design()
	d.Clusters = nil
	d.Independence.Assessment = "justified"
	d.Independence.Justification = "Independent synthetic test clusters only; no live reliability claim."
	for i := range n {
		d.Clusters = append(d.Clusters, releasepolicy.Cluster{ID: fmt.Sprintf("cluster-%d", i),
			Weight: json.Number(fmt.Sprintf("%.17g", 1/float64(n))),
			Tasks:  []releasepolicy.PlannedTask{{ID: fmt.Sprintf("task-%d", i), Weight: "1", TrialOrdinals: []int{1}}}})
	}
	req := requirements()
	req.Assurance = true
	policy, err := Plan(source, source, d, req, nil)
	require.NoError(t, err)
	prepared, err := prepare(source)
	require.NoError(t, err)
	resolved, err := evidence.JSONDigest(prepared.cfg.Spec())
	require.NoError(t, err)
	executable, err := assurance.CurrentExecutableSHA256()
	require.NoError(t, err)
	if implementation != "" {
		executable = implementation
	}
	labels := assurance.ReferenceDocument{Kind: assurance.ReferenceKind, SchemaVersion: "1.0", ID: "synthetic-labels", Version: "1",
		EvalSourceSHA256: releasepolicy.SourceDigest(data).SHA256, EvalResolvedConfigSHA256: resolved.SHA256,
		ImplementationExecutableSHA256: &executable,
		Domains:                        []assurance.DomainCriteria{{ID: "text", MinimumCases: n * 4, MinimumAgreement: 1}}}
	root := t.TempDir()
	for i := range n {
		id := fmt.Sprintf("task-%d", i)
		task := prepared.tasks[id]
		taskDigest, err := evidence.JSONDigest(task)
		require.NoError(t, err)
		decl, err := evidence.JSONDigest(task.Validators[0])
		require.NoError(t, err)
		for j, text := range []string{"hello", "alternate route hello", "wrong", "absent"} {
			caseID := fmt.Sprintf("case-%d-%d", i, j)
			input, err := assurance.NewAuthoredOutput(labels.ID, caseID, id, i*4+j+1, &text)
			require.NoError(t, err)
			name := caseID + ".json"
			require.NoError(t, os.WriteFile(filepath.Join(root, name), input.Document(), 0600))
			ref, err := evidence.Reference(input.Manifest(), "authored-output")
			require.NoError(t, err)
			ref.Pointer = "/output"
			class := assurance.CaseCriticalBad
			switch j {
			case 0:
				class = assurance.CaseGood
			case 1:
				class = assurance.CaseAlternative
			}
			pass := j < 2
			labels.Cases = append(labels.Cases, assurance.ReferenceCase{ID: caseID, ScenarioID: id, Domain: "text",
				Classification: class, TaskID: id, TaskDeclarationSHA256: taskDigest.SHA256,
				AuthoredInput:  &assurance.AuthoredSource{Path: name, DocumentSHA256: releasepolicy.SourceDigest(input.Document()).SHA256},
				ManifestSHA256: input.Manifest().SHA256,
				Checks: []assurance.ReferenceCheck{{RequirementID: "output", Check: task.Requirements[0].Checks[0],
					GraderDeclarationSHA256: decl.SHA256, ExpectedPassed: &pass, Evidence: []models.EvidenceReference{ref}}}})
		}
	}
	labelBytes, err := json.Marshal(labels)
	require.NoError(t, err)
	labelPath := filepath.Join(root, "labels.json")
	require.NoError(t, os.WriteFile(labelPath, labelBytes, 0600))
	state := assurance.ReviewUnreviewed
	reviewer := ""
	reviewedAt := time.Time{}
	if reviewed {
		// This supplied declaration is synthetic test data, not human review.
		state, reviewer, reviewedAt = assurance.ReviewReviewed, "synthetic-test-reviewer", time.Now().Add(-time.Hour).UTC()
	}
	reviewBytes, err := json.Marshal(assurance.ReviewDocument{Kind: assurance.ReviewKind, SchemaVersion: "1.0",
		SourceID: "synthetic-current-source", SubjectID: labels.ID, SubjectVersion: labels.Version,
		LabelsSHA256: releasepolicy.SourceDigest(labelBytes).SHA256, State: state, Reviewer: reviewer, ReviewedAt: reviewedAt})
	require.NoError(t, err)
	reviewPath := filepath.Join(root, "review.json")
	require.NoError(t, os.WriteFile(reviewPath, reviewBytes, 0600))
	return AssuranceSource{Source: source, ReferencesPath: labelPath, ReviewPath: reviewPath,
		AcceptReviewSource: "synthetic-current-source"}, policy
}

func TestAssuredCollectionRealOfflineVerifierAndRegrade(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable collection unsupported")
	}
	source, policy := assuredFixture(t, 8, true)
	contract, err := PlanAssurance(t.Context(), policy, source, source)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, CollectWithAssurance(t.Context(), policy, contract, source, source, dir))
	base, err := releasepolicy.ReadDecision(dir)
	require.NoError(t, err)
	require.False(t, base.Accepted)
	require.Equal(t, "not_assessed", base.Assurance.State)
	result, err := AssessWithAssurance(t.Context(), policy, contract, dir, source, source)
	require.NoError(t, err)
	require.True(t, result.Accepted, "%+v", result)
	require.False(t, result.BaseDecision.Accepted)
	require.Equal(t, "passed", result.Assurance.State)
	require.Equal(t, "passed", result.Regrade.State)
	require.Len(t, result.Reports, 2)
}

func TestAssuredUnreviewedRemainsNonpass(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable collection unsupported")
	}
	source, policy := assuredFixture(t, 1, false)
	contract, err := PlanAssurance(t.Context(), policy, source, source)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, CollectWithAssurance(t.Context(), policy, contract, source, source, dir))
	result, err := AssessWithAssurance(t.Context(), policy, contract, dir, source, source)
	require.NoError(t, err)
	require.False(t, result.Accepted)
	require.Equal(t, "not_assessed", result.Assurance.State)
}

func TestAssuredCurrentSourceChangesRejectBeforeCollection(t *testing.T) {
	for _, change := range []string{"eval_bytes", "task", "fixture", "labels", "review", "authored_input", "acceptance"} {
		t.Run(change, func(t *testing.T) {
			source, policy := assuredFixture(t, 1, true)
			contract, err := PlanAssurance(t.Context(), policy, source, source)
			require.NoError(t, err)
			path := ""
			switch change {
			case "eval_bytes":
				path = source.Source.EvalPath
			case "task":
				path = filepath.Join(filepath.Dir(source.Source.EvalPath), "task-0.yaml")
			case "fixture":
				path = filepath.Join(filepath.Dir(source.Source.EvalPath), "fixtures", "sample.txt")
			case "labels":
				path = source.ReferencesPath
			case "review":
				path = source.ReviewPath
			case "authored_input":
				path = filepath.Join(filepath.Dir(source.ReferencesPath), "case-0-0.json")
			case "acceptance":
				source.AcceptReviewSource = ""
			}
			if path != "" {
				if change == "fixture" {
					require.NoError(t, os.WriteFile(path, []byte("changed fixture"), 0600))
				} else {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					if change == "task" {
						data = []byte(strings.ReplaceAll(string(data), "prompt: hello", "prompt: changed"))
					} else {
						data = append(data, '\n')
					}
					require.NoError(t, os.WriteFile(path, data, 0600))
				}
			}
			directory := filepath.Join(t.TempDir(), "collection")
			require.Error(t, CollectWithAssurance(t.Context(), policy, contract, source, source, directory))
			require.NoDirExists(t, directory)
		})
	}
}

func TestAssuredRechecksAfterActualVerifier(t *testing.T) {
	for _, change := range []string{"eval", "labels", "review", "input", "task", "nil_report", "paid_report"} {
		t.Run(change, func(t *testing.T) {
			source, _ := assuredFixture(t, 1, true)
			verify := func(ctx context.Context, req assurance.VerifyRequest) (*assurance.Report, error) {
				report, err := assurance.Verify(ctx, req)
				if err != nil {
					return nil, err
				}
				if change == "nil_report" {
					return nil, nil
				}
				if change == "paid_report" {
					report.SchemaVersion = "1.1"
					return report, nil
				}
				path := map[string]string{"eval": source.Source.EvalPath, "labels": source.ReferencesPath,
					"review": source.ReviewPath, "input": filepath.Join(filepath.Dir(source.ReferencesPath), "case-0-0.json"),
					"task": filepath.Join(filepath.Dir(source.Source.EvalPath), "task-0.yaml")}[change]
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				if change == "task" {
					data = []byte(strings.ReplaceAll(string(data), "prompt: hello", "prompt: changed"))
				} else {
					data = append(data, '\n')
				}
				require.NoError(t, os.WriteFile(path, data, 0600))
				return report, nil
			}
			_, err := prepareAssuranceWithVerifier(t.Context(), source, verify)
			require.Error(t, err)
		})
	}
}

func TestAssuredCurrentReviewRevocationAndArmSwap(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable collection unsupported")
	}
	source, policy := assuredFixture(t, 1, true)
	contract, err := PlanAssurance(t.Context(), policy, source, source)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, CollectWithAssurance(t.Context(), policy, contract, source, source, dir))
	for _, change := range []string{"withheld_acceptance", "other_labels", "missing_review"} {
		t.Run(change, func(t *testing.T) {
			current := source
			switch change {
			case "withheld_acceptance":
				current.AcceptReviewSource = ""
			case "other_labels":
				other, _ := assuredFixture(t, 1, true)
				current.ReferencesPath = other.ReferencesPath
			case "missing_review":
				current.ReviewPath, current.AcceptReviewSource = "", ""
			}
			d, err := AssessWithAssurance(t.Context(), policy, contract, dir, source, current)
			require.NoError(t, err)
			require.False(t, d.Accepted)
			require.Equal(t, "invalid", d.Assurance.State)
		})
	}
}

func TestAssuredFullRowsAndRegradeCannotAdoptPass(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("private durable collection unsupported")
	}
	source, policy := assuredFixture(t, 1, true)
	contract, err := PlanAssurance(t.Context(), policy, source, source)
	require.NoError(t, err)
	p, err := releasepolicy.DecodePolicy(policy)
	require.NoError(t, err)
	c, err := releasepolicy.DecodeAssuranceContract(contract, p)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "collection")
	require.NoError(t, CollectWithAssurance(t.Context(), policy, contract, source, source, dir))
	ledger, results, err := releasepolicy.ReadAssuranceLedger(dir, p, c)
	require.NoError(t, err)
	actual := results[releasepolicy.Candidate]
	actual.Rows[0].Run.FinalOutput = "wrong"
	raw, err := json.Marshal(actual)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "candidate.results.json"), raw, 0600))
	d, err := AssessWithAssurance(t.Context(), policy, contract, dir, source, source)
	require.Error(t, err)
	require.False(t, d.Accepted)
	ledger.RawResults[releasepolicy.Candidate] = releasepolicy.SourceDigest(raw)
	for i := range ledger.Rows {
		if ledger.Rows[i].Key.Arm == releasepolicy.Candidate {
			rowDigest, err := evidence.JSONDigest(actual.Rows[0])
			require.NoError(t, err)
			ledger.Rows[i].RowDigest = *rowDigest
			ledger.Rows[i].Output.Value = &actual.Rows[0].Run.FinalOutput
		}
	}
	var tape []byte
	var payload []byte
	for _, row := range ledger.Rows {
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		tape = append(tape, raw...)
		tape = append(tape, '\n')
		actualRow := results[row.Key.Arm].Rows[0]
		if row.Key.Arm == releasepolicy.Candidate {
			actualRow = actual.Rows[0]
		}
		raw, err = json.Marshal(actualRow)
		require.NoError(t, err)
		payload = append(payload, raw...)
		payload = append(payload, '\n')
	}
	ledger.StreamDigest = releasepolicy.SourceDigest(tape)
	ledgerBytes, err := releasepolicy.SealJSON(ledger)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assurance.ndjson"), tape, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assurance-rows.ndjson"), payload, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "assurance-ledger.json"), ledgerBytes, 0600))
	d, err = AssessWithAssurance(t.Context(), policy, contract, dir, source, source)
	require.NoError(t, err)
	require.False(t, d.Accepted)
	require.Equal(t, "mismatched", d.Regrade.State)
}

func TestAssuredBuiltCLIWorkflow(t *testing.T) {
	binary := os.Getenv("WAZA_ASSURANCE_CLI")
	if binary == "" {
		t.Skip("manual built-CLI smoke requires WAZA_ASSURANCE_CLI")
	}
	bytes, err := os.ReadFile(binary)
	require.NoError(t, err)
	sum := sha256.Sum256(bytes)
	source, generated := assuredFixtureForExecutable(t, 8, true, hex.EncodeToString(sum[:]))
	p, err := releasepolicy.DecodePolicy(generated)
	require.NoError(t, err)
	p.Design.Allocation = releasepolicy.Allocation{}
	root := t.TempDir()
	for name, value := range map[string]any{"design.json": p.Design, "requirements.json": p.Requirements} {
		data, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, name), data, 0600))
	}
	policyPath, contractPath := filepath.Join(root, "policy.json"), filepath.Join(root, "contract.json")
	sources := []string{"--baseline-eval", source.Source.EvalPath, "--candidate-eval", source.Source.EvalPath}
	references := []string{"--baseline-references", source.ReferencesPath, "--candidate-references", source.ReferencesPath,
		"--baseline-review", source.ReviewPath, "--candidate-review", source.ReviewPath,
		"--baseline-accept-review-source", source.AcceptReviewSource, "--candidate-accept-review-source", source.AcceptReviewSource}
	run := func(args ...string) []byte {
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Env = append(os.Environ(), "NO_COLOR=1", "WAZA_NO_UPDATE_CHECK=1")
		out, err := command.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return out
	}
	run(append(append([]string{"compare-plan"}, sources...), "--design", filepath.Join(root, "design.json"),
		"--requirements", filepath.Join(root, "requirements.json"), "--output", policyPath)...)
	selected := append(append([]string{}, sources...), references...)
	run(append(append([]string{"compare-assurance-plan"}, selected...), "--release-policy", policyPath, "--output", contractPath)...)
	dir := filepath.Join(root, "collection")
	selected = append(selected, "--release-policy", policyPath, "--assurance-contract", contractPath, "--collection-dir", dir)
	run(append([]string{"compare-collect"}, selected...)...)
	for _, command := range []string{"gate", "compare"} {
		out := run(append(append([]string{command}, selected...), "--format=json")...)
		var decision releasepolicy.AssuredDecision
		require.NoError(t, json.Unmarshal(out, &decision))
		require.True(t, decision.Accepted, "%s", out)
		require.False(t, decision.BaseDecision.Accepted)
		require.Equal(t, "not_assessed", decision.BaseDecision.Assurance.State)
	}
	base := exec.CommandContext(t.Context(), binary, "gate", "--release-policy", policyPath, "--collection-dir", dir, "--format=json")
	base.Env = append(os.Environ(), "NO_COLOR=1", "WAZA_NO_UPDATE_CHECK=1")
	out, err := base.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 1, exit.ExitCode())
	require.Contains(t, string(out), `"not_assessed"`)
	require.Contains(t, string(out), `"accepted": false`)
}
