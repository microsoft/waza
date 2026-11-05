package orchestration

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

func encodeCallbackFreeRequest(req *execution.ExecutionRequest) ([]byte, error) {
	if req == nil || req.PermissionHandler != nil || len(req.Tools) != 0 {
		return nil, fmt.Errorf("request contains callbacks or custom tools")
	}
	// Function-typed SDK members cannot be encoded, even when nil. Bind their
	// known absence and retain every other exported native request field.
	type request execution.ExecutionRequest
	value := struct {
		*request
		PermissionHandler any `json:"PermissionHandler"`
		Tools             any `json:"Tools"`
	}{request: (*request)(req)}
	return json.Marshal(value)
}

// FreezeCapturedNativeRequest encodes all native CLIENT request fields, not the
// narrower nativetask intent projection and not a runtime/provider identity.
func FreezeCapturedNativeRequest(req *execution.ExecutionRequest) ([]byte, error) {
	if req == nil || !req.NoSkills || !req.SkipWorkspaceCapture || req.EphemeralSession ||
		req.ToolPolicy == nil || req.ToolPolicy.Mode != execution.ToolPolicyDenyAll ||
		req.SessionID != "" || req.WorkspaceDir != "" || len(req.SkillPaths) != 0 ||
		len(req.MCPServers) != 0 || len(req.GitResources) != 0 || len(req.CommandMocks) != 0 {
		return nil, fmt.Errorf("unsupported captured native request")
	}
	return encodeCallbackFreeRequest(req)
}

// RequestFiles separates native request construction from filesystem access.
// Snapshot callers supply captured, inert bytes; ordinary runners retain the
// exact native filesystem behavior, including resource-loading warnings.
type RequestFiles interface {
	ReadFile(string) ([]byte, error)
	Stat(string) (os.FileInfo, error)
	EvalSymlinks(string) (string, error)
	WalkDir(string, fs.WalkDirFunc) error
}

type nativeRequestFiles struct{}

func (nativeRequestFiles) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (nativeRequestFiles) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (nativeRequestFiles) EvalSymlinks(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}
func (nativeRequestFiles) WalkDir(path string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(path, fn)
}

func (r *EvalRunner) inputFiles() RequestFiles {
	if r.requestFiles != nil {
		return r.requestFiles
	}
	return nativeRequestFiles{}
}

func (r *EvalRunner) setRequestInputRole(role string) {
	if files, ok := r.requestFiles.(interface{ SetInputRole(string) }); ok {
		files.SetInputRole(role)
	}
}

// ValidateNativeSnapshotConfiguration is declaration support, not runtime
// admission. It neither constructs an engine nor assesses ambient instructions.
func ValidateNativeSnapshotConfiguration(spec *models.EvalSpec, tc *models.TestCase) error {
	if spec == nil || tc == nil {
		return fmt.Errorf("native snapshot requires spec and task")
	}
	model := strings.TrimSpace(spec.Config.ModelID)
	if spec.Config.EngineType != "copilot-sdk" || model == "" || model != spec.Config.ModelID ||
		strings.EqualFold(model, "auto") || strings.EqualFold(model, "unknown") ||
		!spec.SkillsDisabledForTask(tc.SkillPaths) || spec.SkillName != "" ||
		len(spec.Config.SkillPaths) != 0 || len(tc.SkillPaths) != 0 ||
		len(spec.Config.RequiredSkills) != 0 || spec.Config.InjectSkillBody != nil ||
		spec.Config.TriggerSkillRouting {
		return fmt.Errorf("unsupported native model/skill/agent configuration")
	}
	if spec.Baseline || spec.Config.Concurrent || spec.Config.Workers != 0 ||
		!reflect.ValueOf(spec.Hooks).IsZero() || len(spec.MCPMocks) != 0 ||
		len(spec.Config.ServerConfigs) != 0 || spec.CommandMocks != nil ||
		tc.CommandMocks != nil || spec.Adversarial != nil || spec.TasksFrom != "" ||
		len(spec.Inputs) != 0 || spec.Range != [2]int{} ||
		len(tc.Checkpoints) != 0 || tc.Stimulus.Responder != nil ||
		len(tc.Stimulus.FollowUps) != 0 || len(tc.Stimulus.Repos) != 0 ||
		len(tc.Stimulus.Environment) != 0 || len(tc.Requirements) != 0 ||
		spec.Config.JudgeModel != "" || spec.Config.JudgeReasoningEffort != "" {
		return fmt.Errorf("unsupported native dependency/parallel/hooks/multiturn configuration")
	}
	if !reflect.ValueOf(tc.Expectation).IsZero() {
		return fmt.Errorf("unsupported native implicit expectation projection")
	}
	seen := map[string]bool{}
	for _, g := range spec.Graders {
		if _, ok := g.Parameters.(models.TextGraderParameters); !ok ||
			g.Kind != models.GraderKindText || g.Identifier == "" || seen[g.Identifier] ||
			g.Ref != "" || g.ScriptPath != "" || g.Rubric != "" || g.ModelID != "" {
			return fmt.Errorf("unsupported native grader declaration")
		}
		seen[g.Identifier] = true
	}
	for _, g := range tc.Validators {
		if _, ok := g.Parameters.(models.TextGraderParameters); !ok ||
			g.Kind != models.GraderKindText || g.Identifier == "" || seen[g.Identifier] ||
			g.Rubric != "" || len(g.Checks) != 0 {
			return fmt.Errorf("unsupported native task grader declaration")
		}
		seen[g.Identifier] = true
	}
	if len(seen) == 0 {
		return fmt.Errorf("native snapshot requires an independent text grader")
	}
	return tc.ValidateForExecutor(spec.Config.EngineType)
}

// BuildCapturedNativeRequest uses the ordinary runner constructor logic, with
// no runner/engine initialization. Only selected-profile policy defaults differ.
func BuildCapturedNativeRequest(spec *models.EvalSpec, tc *models.TestCase, specDir, contextDir string, files RequestFiles) (*execution.ExecutionRequest, error) {
	if err := ValidateNativeSnapshotConfiguration(spec, tc); err != nil {
		return nil, err
	}
	if files == nil {
		return nil, fmt.Errorf("native request requires captured files")
	}
	for _, ref := range tc.Stimulus.Resources {
		if ref.Location == "" || (ref.Body == "" && contextDir == "" && tc.ContextRoot == "") {
			return nil, fmt.Errorf("native resource requires a destination and actual context source")
		}
		if ref.Body == "" && (filepath.IsAbs(ref.Location) || strings.Contains(filepath.Clean(ref.Location), "..")) {
			return nil, fmt.Errorf("native resource path %q would be rejected by the native loader", ref.Location)
		}
	}
	r := &EvalRunner{cfg: config.NewEvalConfig(spec, config.WithSpecDir(specDir), config.WithFixtureDir(contextDir)), requestFiles: files}
	req, err := r.buildExecutionRequest(tc)
	if err != nil {
		return nil, err
	}
	req.ToolPolicy = &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll}
	req.SkipWorkspaceCapture = true
	req.CommandMocksBaseDir = ""
	return req, nil
}
