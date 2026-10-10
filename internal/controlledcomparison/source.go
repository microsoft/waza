// Package controlledcomparison is the source-bound, offline-only producer for
// explicit release policies. Ordinary evaluation capabilities remain unchanged.
package controlledcomparison

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/releasepolicy"
)

type Source struct {
	EvalPath   string
	ContextDir string
}

type preparedPlan struct {
	plan   releasepolicy.PolicyArm
	cfg    *config.EvalConfig
	tasks  map[string]*models.TestCase
	inputs map[string]*orchestration.ControlledInput
	golden []string
}

type fileIdentity struct {
	Path   string                `json:"path"`
	Digest models.EvidenceDigest `json:"source_digest"`
}

func bytesDigest(data []byte) models.EvidenceDigest {
	hash := sha256.Sum256(data)
	return models.EvidenceDigest{SHA256: hex.EncodeToString(hash[:]), Encoding: "source-bytes"}
}

func executableIdentity() (fileIdentity, error) {
	data, err := executableBytes()
	if err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{Path: "waza_executable", Digest: bytesDigest(data)}, nil
}

func executableBytes() ([]byte, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating actual executable: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading actual executable identity: %w", err)
	}
	return data, nil
}

func prepare(source Source) (*preparedPlan, error) {
	report := preflight.Inspect(source.EvalPath, preflight.Options{ContextDir: source.ContextDir})
	if !report.Complete || report.Failed(false) {
		return nil, fmt.Errorf("source inventory is incomplete or structurally invalid: %v", report.Diagnostics)
	}
	spec, err := models.LoadEvalSpecOffline(source.EvalPath)
	if err != nil {
		return nil, err
	}
	if spec.TasksFrom != "" || len(spec.Inputs) != 0 || spec.Range != [2]int{} || spec.Adversarial != nil {
		return nil, fmt.Errorf("controlled source binding does not support generated/template/range/adversarial tasks")
	}
	dir, err := filepath.Abs(filepath.Dir(source.EvalPath))
	if err != nil {
		return nil, err
	}
	contextDir := source.ContextDir
	if contextDir == "" {
		contextDir = filepath.Join(dir, "fixtures")
	}
	contextDir, err = filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(dir), config.WithFixtureDir(contextDir))
	prepared := &preparedPlan{cfg: cfg, tasks: map[string]*models.TestCase{},
		inputs: map[string]*orchestration.ControlledInput{}, golden: []string{}}
	executable, err := executableBytes()
	if err != nil {
		return nil, err
	}
	projectionInput := controlledprojection.SuiteInput{Spec: *spec, Executable: executable,
		Tasks: []controlledprojection.TaskInput{}}
	for _, task := range report.Tasks {
		if !task.Enabled {
			continue
		}
		tc, err := models.LoadTestCaseOffline(task.Source)
		if err != nil {
			return nil, fmt.Errorf("loading controlled task %s: %w", task.ID, err)
		}
		// Use the inspector's actual cwd-relative task context resolution.
		tc.ContextRoot = task.ContextDir
		if prepared.tasks[tc.TestID] != nil {
			return nil, fmt.Errorf("duplicate controlled task: %s", tc.TestID)
		}
		runner := orchestration.NewEvalRunner(cfg, nil)
		input, err := runner.PrepareControlledInput(tc)
		if err != nil {
			return nil, fmt.Errorf("binding controlled task %s: %w", tc.TestID, err)
		}
		request, err := input.View()
		if err != nil {
			return nil, err
		}
		projectionInput.Tasks = append(projectionInput.Tasks,
			controlledprojection.TaskInput{Definition: *tc, Request: *request})
		prepared.tasks[tc.TestID], prepared.inputs[tc.TestID] = tc, input
	}
	if len(prepared.tasks) == 0 {
		return nil, fmt.Errorf("controlled collection requires enabled inspected tasks")
	}
	// Native text admission excluded every external/script/rubric grader.
	// A present lock file is still bound, even if no grader uses its entries.
	lockPath := filepath.Join(dir, models.LockfileName)
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspecting lock applicability: %w", err)
	}
	projectionInput.LockPresent, projectionInput.LockBytes = err == nil, lockBytes
	projection, err := controlledprojection.MockPlan(projectionInput)
	if err != nil {
		return nil, err
	}
	prepared.plan, prepared.golden = projection.Arm, projection.GoldenIDs
	return prepared, nil
}

// Plan inspects sources without initializing engines or collecting outcomes.
// A design is mandatory; its independence declaration is never inferred from
// task names, retries, repeated trials, static readiness or mock labels.
func Plan(baseline, candidate Source, design releasepolicy.Design, requirements releasepolicy.Requirements, allowedFields []string) ([]byte, error) {
	sources := map[releasepolicy.Arm]Source{releasepolicy.Baseline: baseline, releasepolicy.Candidate: candidate}
	p := releasepolicy.Policy{Kind: releasepolicy.PolicyKind, Version: releasepolicy.Version,
		Design: design, Requirements: requirements, Arms: map[releasepolicy.Arm]releasepolicy.PolicyArm{},
		Changes: []releasepolicy.AllowedChange{}, GoldenIDs: []string{},
		GoldenRule: "both_arms_first_attempt_pass", FamilyRule: "single_contrast_external_family_bound"}
	if design.Allocation.Mechanism != "" || design.Allocation.Seed != "" || len(design.Allocation.Assignments) != 0 {
		return nil, fmt.Errorf("source planning generates allocation once; input design allocation must be empty")
	}
	allowed := map[string]bool{}
	for _, field := range allowedFields {
		if (field != "engine" && field != "model" && field != "reasoning_effort") || allowed[field] {
			return nil, fmt.Errorf("unknown or duplicate allowed field: %s", field)
		}
		allowed[field] = true
	}
	projections := map[releasepolicy.Arm]controlledprojection.SourceProjection{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		prepared, err := prepare(sources[arm])
		if err != nil {
			return nil, fmt.Errorf("%s source: %w", arm, err)
		}
		p.Arms[arm] = prepared.plan
		projections[arm] = controlledprojection.SourceProjection{Arm: prepared.plan, GoldenIDs: prepared.golden}
	}
	for _, before := range p.Arms[releasepolicy.Baseline].Plan.Tasks {
		for _, after := range p.Arms[releasepolicy.Candidate].Plan.Tasks {
			if before.ID != after.ID {
				continue
			}
			for _, pair := range []struct{ field, before, after string }{
				{"engine", before.Settings.Engine, after.Settings.Engine},
				{"model", before.Settings.Model, after.Settings.Model},
				{"reasoning_effort", before.Settings.ReasoningEffort, after.Settings.ReasoningEffort},
			} {
				if pair.before == pair.after {
					continue
				}
				if !allowed[pair.field] {
					return nil, fmt.Errorf("undeclared %s change for %s", pair.field, before.ID)
				}
				p.Changes = append(p.Changes, releasepolicy.AllowedChange{TaskID: before.ID,
					Field: pair.field, Before: pair.before, After: pair.after})
			}
		}
	}
	var err error
	p.GoldenIDs, err = controlledprojection.GoldenUnion(projections[releasepolicy.Baseline], projections[releasepolicy.Candidate])
	if err != nil {
		return nil, err
	}
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("allocating paired arm order: %w", err)
	}
	p.Design.Allocation = releasepolicy.Allocation{Mechanism: "sha256_seed_cluster_first_bit",
		Seed: hex.EncodeToString(seed[:]), Assignments: []releasepolicy.Assignment{}}
	for _, cluster := range design.Clusters {
		order, err := releasepolicy.ArmOrder(p.Design.Allocation.Seed, cluster.ID)
		if err != nil {
			return nil, err
		}
		p.Design.Allocation.Assignments = append(p.Design.Allocation.Assignments, releasepolicy.Assignment{
			ClusterID: cluster.ID, Order: order})
	}
	data, err := releasepolicy.SealJSON(p)
	if err != nil {
		return nil, err
	}
	if _, err := releasepolicy.DecodePolicy(data); err != nil {
		return nil, err
	}
	return data, nil
}
