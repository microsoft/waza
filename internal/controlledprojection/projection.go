// Package controlledprojection computes source-time identities from typed,
// already prepared inputs. Its values provide neither source custody nor
// runtime, dependency, admission, or execution authority.
package controlledprojection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"path/filepath"
	"sort"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
)

type TaskInput struct {
	Definition models.TestCase
	Request    execution.ExecutionRequest
}

type SuiteInput struct {
	Spec        models.EvalSpec
	Tasks       []TaskInput // Enabled tasks in their actual owning traversal order.
	Executable  []byte
	LockPresent bool
	LockBytes   []byte
}

type SourceProjection struct {
	Arm       releasepolicy.PolicyArm
	GoldenIDs []string
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

// MockPlan mechanically preserves the legacy projection, including its numeric
// representation and lack of new size limits. Source guards remain with prepare.
func MockPlan(input SuiteInput) (SourceProjection, error) {
	return plan(input, false)
}

// NativePlan validates only the offline native declaration/request subset.
// Runtime labels are unavailable, not an observation of the executable/provider.
func NativePlan(input SuiteInput) (SourceProjection, error) {
	if err := validateNative(input, nativeLimits); err != nil {
		return SourceProjection{}, err
	}
	result, err := plan(input, true)
	if err != nil {
		return SourceProjection{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return SourceProjection{}, fmt.Errorf("encoding native source projection: %w", err)
	}
	if len(raw) > nativeLimits.projection {
		return SourceProjection{}, fmt.Errorf("native source projection exceeds bounded limit")
	}
	return result, nil
}

func plan(input SuiteInput, native bool) (SourceProjection, error) {
	spec := input.Spec
	result := SourceProjection{GoldenIDs: []string{}, Arm: releasepolicy.PolicyArm{
		Plan: releasepolicy.ResolvedPlan{Kind: releasepolicy.PlanKind, Version: releasepolicy.Version,
			Tasks: []releasepolicy.TaskPlan{}, Identities: []releasepolicy.Identity{}}}}
	executable := fileIdentity{Path: "waza_executable", Digest: bytesDigest(input.Executable)}
	implementation := "sha256:" + executable.Digest.SHA256
	for _, task := range input.Tasks {
		tc, request := task.Definition, task.Request
		settings := spec.Config
		settings.EngineType, settings.ModelID, settings.ReasoningEffort = "", "", ""
		settings.InstructionFiles = nil
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
			return SourceProjection{}, err
		}
		runtime := releasepolicy.ExpectedRuntime{Availability: "expected",
			EngineImplementation: implementation, ModelVersion: "mock_no_provider"}
		mode := "mock_without_external_dependencies"
		if native {
			runtime = releasepolicy.ExpectedRuntime{Availability: "unavailable",
				Reason: "runtime implementation and model version not observed by offline native snapshot"}
			mode = "copilot_sdk_denied_tools_no_declared_dependencies"
		}
		result.Arm.Plan.Tasks = append(result.Arm.Plan.Tasks, releasepolicy.TaskPlan{
			ID: tc.TestID, Settings: releasepolicy.Settings{
				Engine: spec.Config.EngineType, Model: request.ModelID, ReasoningEffort: request.ReasoningEffort,
				JudgeModel: settings.JudgeModel, JudgeReasoningEffort: settings.JudgeReasoningEffort,
				TimeoutSeconds: timeout, MaxAttempts: max(1, spec.Config.MaxAttempts),
				TrialsPerTask: spec.Config.TrialsPerTask, NoSkills: request.NoSkills, OtherSettingsDigest: *other},
			ExpectedRuntime: runtime})
		definition := tc
		definition.ContextRoot = "@resolved_context"
		definition.InstructionFiles = nil
		resources := make([]fileIdentity, 0, len(request.Resources))
		for _, resource := range request.Resources {
			resources = append(resources, fileIdentity{Path: filepath.ToSlash(resource.Path), Digest: bytesDigest(resource.Content)})
		}
		instructions := make([]fileIdentity, 0, len(request.Instructions))
		for _, instruction := range request.Instructions {
			instructions = append(instructions, fileIdentity{Path: filepath.ToSlash(instruction.Path), Digest: bytesDigest(instruction.Content)})
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
			}{mode, request.NoSkills}},
		} {
			identity, err := project(projection.domain, tc.TestID, projection.value)
			if err != nil {
				return SourceProjection{}, err
			}
			result.Arm.Plan.Identities = append(result.Arm.Plan.Identities, identity)
		}
		if tc.Golden {
			result.GoldenIDs = append(result.GoldenIDs, tc.TestID)
		}
	}
	identity, err := project("grader_implementation", "", executable)
	if err != nil {
		return SourceProjection{}, err
	}
	result.Arm.Plan.Identities = append(result.Arm.Plan.Identities, identity)
	for _, domain := range []string{"rubric_inventory", "lock_inventory"} {
		if domain == "lock_inventory" && input.LockPresent {
			identity, err := project(domain, "", []fileIdentity{{Path: models.LockfileName, Digest: bytesDigest(input.LockBytes)}})
			if err != nil {
				return SourceProjection{}, err
			}
			result.Arm.Plan.Identities = append(result.Arm.Plan.Identities, identity)
			continue
		}
		digest, err := releasepolicy.EmptyIdentity(domain)
		if err != nil {
			return SourceProjection{}, err
		}
		result.Arm.Plan.Identities = append(result.Arm.Plan.Identities,
			releasepolicy.Identity{Domain: domain, Availability: "not_applicable", Digest: &digest})
	}
	digest, err := evidence.JSONDigest(result.Arm.Plan)
	if err != nil {
		return SourceProjection{}, err
	}
	result.Arm.Digest = *digest
	return result, nil
}

// GoldenUnion preserves the planner's sorted, nonnil both-arm union. It does not
// normalize a policy's supplied list or validate source/policy matching.
func GoldenUnion(baseline, candidate SourceProjection) ([]string, error) {
	if baseline.GoldenIDs == nil || candidate.GoldenIDs == nil ||
		!complete(baseline.Arm) || !complete(candidate.Arm) {
		return nil, fmt.Errorf("golden union requires complete both-arm projections")
	}
	seen := map[string]bool{}
	for _, ids := range [][]string{baseline.GoldenIDs, candidate.GoldenIDs} {
		for _, id := range ids {
			seen[id] = true
		}
	}
	ids := []string{}
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func complete(arm releasepolicy.PolicyArm) bool {
	if arm.Plan.Kind != releasepolicy.PlanKind || arm.Plan.Version != releasepolicy.Version ||
		arm.Plan.Tasks == nil || arm.Plan.Identities == nil {
		return false
	}
	digest, err := evidence.JSONDigest(arm.Plan)
	return err == nil && *digest == arm.Digest
}

type limits struct {
	source, executable, total, projection, tasks, entries int
}

// These limits apply only to G1 native intermediate material. Raw eval/task
// source sizes and directory-entry custody still belong to snapshot capture.
var nativeLimits = limits{16 << 20, 128 << 20, 256 << 20, 16 << 20, 1024, 16384}

func validateNative(input SuiteInput, bound limits) error {
	if len(input.Tasks) == 0 || len(input.Tasks) > bound.tasks {
		return fmt.Errorf("native task limit exceeded or no enabled tasks")
	}
	if len(input.Executable) == 0 || len(input.Executable) > bound.executable {
		return fmt.Errorf("native executable source limit exceeded or empty")
	}
	if !input.LockPresent && len(input.LockBytes) != 0 {
		return fmt.Errorf("native absent lock has source bytes")
	}
	total, entries, projectionSize := len(input.Executable), 1, 0
	addSource := func(data []byte) error {
		if len(data) > bound.source {
			return fmt.Errorf("native source limit exceeded")
		}
		if len(data) > bound.total-total {
			return fmt.Errorf("native aggregate source byte limit exceeded")
		}
		if entries >= bound.entries {
			return fmt.Errorf("native source entry limit exceeded")
		}
		total += len(data)
		entries++
		return nil
	}
	if total > bound.total || entries > bound.entries {
		return fmt.Errorf("native aggregate source byte or entry limit exceeded")
	}
	if input.LockPresent {
		if err := addSource(input.LockBytes); err != nil {
			return err
		}
	}
	addProjection := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encoding native declaration projection: %w", err)
		}
		if len(raw) > bound.projection-projectionSize {
			return fmt.Errorf("native declaration/request projection exceeds bounded limit")
		}
		projectionSize += len(raw)
		return nil
	}
	// Reject dependency/custom-grader declarations before JSON encoding can
	// reach an unsupported interface-valued declaration.
	if err := orchestration.ValidateNativeSnapshotConfiguration(&input.Spec, &input.Tasks[0].Definition); err != nil {
		return err
	}
	if err := addProjection(input.Spec); err != nil {
		return err
	}
	if err := input.Spec.Validate(); err != nil {
		return fmt.Errorf("validating native eval declaration: %w", err)
	}
	seen := map[string]bool{}
	for _, task := range input.Tasks {
		tc, req := task.Definition, task.Request
		if tc.TestID == "" || seen[tc.TestID] || (tc.Active != nil && !*tc.Active) {
			return fmt.Errorf("native projection requires unique enabled task IDs")
		}
		seen[tc.TestID] = true
		if err := orchestration.ValidateNativeSnapshotConfiguration(&input.Spec, &tc); err != nil {
			return err
		}
		if err := tc.Validate(); err != nil {
			return fmt.Errorf("validating native task declaration: %w", err)
		}
		for _, value := range []any{tc.Stimulus.Metadata, req.Context} {
			if err := validateMetadata(value); err != nil {
				return err
			}
			if _, err := evidence.JSONDigest(value); err != nil {
				return fmt.Errorf("validating native metadata projection: %w", err)
			}
		}
		raw, err := orchestration.FreezeCapturedNativeRequest(&req)
		if err != nil {
			return err
		}
		first := input.Spec.Config.FirstEventTimeoutSec
		if tc.FirstEventTimeoutSec != nil {
			first = *tc.FirstEventTimeoutSec
		}
		if req.ModelID != input.Spec.Config.ModelID || req.ReasoningEffort != input.Spec.Config.ReasoningEffort ||
			req.FirstEventTimeout < 0 || req.FirstEventTimeout != time.Duration(max(0, first))*time.Second ||
			req.Streaming || req.MessageMode != "" || req.CancelOnSkillInvocation ||
			req.TriggerSkillRouting || req.SuppressSkillBody || req.SkillName != "" || req.CommandMocksBaseDir != "" {
			return fmt.Errorf("unsupported native request settings")
		}
		for _, resource := range req.Resources {
			if err := addSource(resource.Content); err != nil {
				return err
			}
		}
		for _, instruction := range req.Instructions {
			if err := addSource(instruction.Content); err != nil {
				return err
			}
		}
		if err := addProjection(tc); err != nil {
			return err
		}
		if len(raw) > bound.projection-projectionSize {
			return fmt.Errorf("native declaration/request projection exceeds bounded limit")
		}
		projectionSize += len(raw)
	}
	return nil
}

// Captured source metadata contains exact integers, never authored floats or
// float-rounded overflow. Check it without converting or rewriting any token.
func validateMetadata(value any) error {
	return validateMetadataValue(value, 0)
}

func validateMetadataValue(value any, depth int) error {
	// Match JSON's own maximum nesting depth; reject cycles/overdeep typed
	// values before encoding or invoking an unsupported custom marshaler.
	if depth > 10000 {
		return fmt.Errorf("unsupported native metadata nesting or cycle")
	}
	switch value := value.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nil
	case json.Number:
		integer, ok := new(big.Int).SetString(string(value), 10)
		if !ok || (integer.Sign() >= 0 && integer.BitLen() > 64) ||
			(integer.Sign() < 0 && integer.Cmp(new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), 63))) < 0) {
			return fmt.Errorf("unsupported native metadata number")
		}
		// Marshal checks the original JSON token grammar without normalization.
		if _, err := json.Marshal(value); err != nil {
			return fmt.Errorf("unsupported native metadata number: %w", err)
		}
		return nil
	case map[string]any:
		for _, entry := range value {
			if err := validateMetadataValue(entry, depth+1); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, entry := range value {
			if err := validateMetadataValue(entry, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported native metadata type %T; exact captured integers required", value)
	}
}
