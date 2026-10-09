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
	"sort"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/evidence"
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

func project(domain, taskID string, value any) (releasepolicy.Identity, error) {
	digest, err := evidence.JSONDigest(struct {
		Domain string `json:"domain"`
		Value  any    `json:"value"`
	}{domain, value})
	if err != nil {
		return releasepolicy.Identity{}, fmt.Errorf("projecting %s/%s: %w", domain, taskID, err)
	}
	return releasepolicy.Identity{Domain: domain, TaskID: taskID, Availability: "available", Digest: digest}, nil
}

func executableIdentity() (fileIdentity, error) {
	path, err := os.Executable()
	if err != nil {
		return fileIdentity{}, fmt.Errorf("locating actual executable: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileIdentity{}, fmt.Errorf("reading actual executable identity: %w", err)
	}
	return fileIdentity{Path: "waza_executable", Digest: bytesDigest(data)}, nil
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
		inputs: map[string]*orchestration.ControlledInput{}, golden: []string{},
		plan: releasepolicy.PolicyArm{Plan: releasepolicy.ResolvedPlan{Kind: releasepolicy.PlanKind,
			Version: releasepolicy.Version, Tasks: []releasepolicy.TaskPlan{}, Identities: []releasepolicy.Identity{}}}}
	executable, err := executableIdentity()
	if err != nil {
		return nil, err
	}
	implementation := "sha256:" + executable.Digest.SHA256
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
		settings := spec.Config
		settings.EngineType, settings.ModelID, settings.ReasoningEffort = "", "", ""
		settings.InstructionFiles = nil // Actual ordered instruction bytes below.
		timeout := spec.Config.TimeoutSec
		if tc.TimeoutSec != nil {
			timeout = *tc.TimeoutSec
		}
		settings.TimeoutSec = timeout
		if tc.FirstEventTimeoutSec != nil {
			settings.FirstEventTimeoutSec = *tc.FirstEventTimeoutSec
		}
		other, err := evidence.JSONDigest(settings)
		if err != nil {
			return nil, err
		}
		maxAttempts := max(1, spec.Config.MaxAttempts)
		prepared.plan.Plan.Tasks = append(prepared.plan.Plan.Tasks, releasepolicy.TaskPlan{
			ID: tc.TestID, Settings: releasepolicy.Settings{
				Engine: spec.Config.EngineType, Model: request.ModelID, ReasoningEffort: request.ReasoningEffort,
				JudgeModel: settings.JudgeModel, JudgeReasoningEffort: settings.JudgeReasoningEffort,
				TimeoutSeconds: timeout, MaxAttempts: maxAttempts, TrialsPerTask: spec.Config.TrialsPerTask,
				NoSkills: request.NoSkills, OtherSettingsDigest: *other},
			ExpectedRuntime: releasepolicy.ExpectedRuntime{Availability: "expected",
				EngineImplementation: implementation, ModelVersion: "mock_no_provider"}})
		definition := *tc
		definition.ContextRoot = "@resolved_context"
		definition.InstructionFiles = nil
		resources := make([]fileIdentity, 0, len(request.Resources))
		for _, resource := range request.Resources {
			resources = append(resources, fileIdentity{Path: filepath.ToSlash(resource.Path), Digest: bytesDigest(resource.Content)})
		}
		// Preserve resource order/multiplicity: these are the actual copied
		// inputs, not a name-only set or an independently read directory.
		instructions := make([]fileIdentity, 0, len(request.Instructions))
		for _, instruction := range request.Instructions {
			instructions = append(instructions, fileIdentity{Path: filepath.ToSlash(instruction.Path),
				Digest: bytesDigest(instruction.Content)})
		}
		for _, projection := range []struct {
			domain string
			value  any
		}{
			{"task_definition", definition},
			{"resolved_prompt", struct {
				Message string         `json:"message"`
				Context map[string]any `json:"context"`
				WorkDir string         `json:"work_dir"`
			}{request.Message, request.Context, request.WorkDir}},
			{"fixture_inventory", resources},
			{"instruction_inventory", instructions},
			{"grader_configuration", struct {
				Eval        []models.GraderConfig    `json:"eval"`
				Task        []models.ValidatorInline `json:"task"`
				Expectation models.TaskExpectation   `json:"expectation"`
			}{spec.Graders, tc.Validators, tc.Expectation}},
			{"dependency_mode", struct {
				Mode     string `json:"mode"`
				NoSkills bool   `json:"no_skills"`
			}{"mock_without_external_dependencies", request.NoSkills}},
		} {
			identity, err := project(projection.domain, tc.TestID, projection.value)
			if err != nil {
				return nil, err
			}
			prepared.plan.Plan.Identities = append(prepared.plan.Plan.Identities, identity)
		}
		prepared.tasks[tc.TestID], prepared.inputs[tc.TestID] = tc, input
		if tc.Golden {
			prepared.golden = append(prepared.golden, tc.TestID)
		}
	}
	if len(prepared.tasks) == 0 {
		return nil, fmt.Errorf("controlled collection requires enabled inspected tasks")
	}
	identity, err := project("grader_implementation", "", executable)
	if err != nil {
		return nil, err
	}
	prepared.plan.Plan.Identities = append(prepared.plan.Plan.Identities, identity)
	// Native text admission excluded every external/script/rubric grader.
	// A present lock file is still bound, even if no grader uses its entries.
	lockPath := filepath.Join(dir, models.LockfileName)
	lockBytes, err := os.ReadFile(lockPath)
	for _, domain := range []string{"rubric_inventory", "lock_inventory"} {
		if domain == "lock_inventory" && err == nil {
			lockIdentity, err := project(domain, "", []fileIdentity{{Path: models.LockfileName, Digest: bytesDigest(lockBytes)}})
			if err != nil {
				return nil, err
			}
			prepared.plan.Plan.Identities = append(prepared.plan.Plan.Identities, lockIdentity)
			continue
		}
		if domain == "lock_inventory" && !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspecting lock applicability: %w", err)
		}
		digest, err := releasepolicy.EmptyIdentity(domain)
		if err != nil {
			return nil, err
		}
		prepared.plan.Plan.Identities = append(prepared.plan.Plan.Identities,
			releasepolicy.Identity{Domain: domain, Availability: "not_applicable", Digest: &digest})
	}
	digest, err := evidence.JSONDigest(prepared.plan.Plan)
	if err != nil {
		return nil, err
	}
	prepared.plan.Digest = *digest
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
	golden := map[string]bool{}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		prepared, err := prepare(sources[arm])
		if err != nil {
			return nil, fmt.Errorf("%s source: %w", arm, err)
		}
		p.Arms[arm] = prepared.plan
		for _, id := range prepared.golden {
			golden[id] = true
		}
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
	for id := range golden {
		p.GoldenIDs = append(p.GoldenIDs, id)
	}
	sort.Strings(p.GoldenIDs)
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
