package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/validation"
	"gopkg.in/yaml.v3"
)

// RunState tracks the status of an eval run.
type RunState struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "running", "completed", "failed", "canceled"
	Error  string `json:"error,omitempty"`
}

// HandlerContext provides shared state for method handlers.
type HandlerContext struct {
	mu   sync.Mutex
	runs map[string]*RunState

	// cancelFuncs tracks cancel functions for running evals.
	cancelFuncs map[string]context.CancelFunc

	nextRunID int
}

// NewHandlerContext creates a new handler context.
func NewHandlerContext() *HandlerContext {
	return &HandlerContext{
		runs:        make(map[string]*RunState),
		cancelFuncs: make(map[string]context.CancelFunc),
	}
}

// RegisterHandlers registers all eval/task/run method handlers.
func RegisterHandlers(registry *MethodRegistry, hctx *HandlerContext) {
	registry.Register("eval.list", hctx.handleEvalList)
	registry.Register("eval.get", hctx.handleEvalGet)
	registry.Register("eval.validate", hctx.handleEvalValidate)
	registry.Register("eval.run", hctx.handleEvalRun)
	registry.Register("task.list", hctx.handleTaskList)
	registry.Register("task.get", hctx.handleTaskGet)
	registry.Register("run.status", hctx.handleRunStatus)
	registry.Register("run.cancel", hctx.handleRunCancel)
}

// --- eval.list ---

type EvalListParams struct {
	Dir string `json:"dir"`
}

type EvalListResult struct {
	Evals []EvalSummary `json:"evals"`
}

type EvalSummary struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	SkillName string `json:"skill,omitempty"`
}

func (h *HandlerContext) handleEvalList(_ context.Context, params json.RawMessage) (any, *Error) {
	var p EvalListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Dir == "" {
		return nil, ErrInvalidParams("dir is required")
	}

	// Walk the directory looking for eval.yaml files
	var evals []EvalSummary
	err := filepath.Walk(p.Dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == "eval.yaml" || base == "eval.yml" {
			spec, loadErr := models.LoadEvalSpec(path)
			summary := EvalSummary{Path: path}
			if loadErr == nil {
				summary.Name = spec.Name
				summary.SkillName = spec.SkillName
			} else {
				summary.Name = path
			}
			evals = append(evals, summary)
		}
		return nil
	})
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	return &EvalListResult{Evals: evals}, nil
}

// --- eval.get ---

type EvalGetParams struct {
	Path string `json:"path"`
}

type EvalGetResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	SkillName   string            `json:"skill,omitempty"`
	Config      models.EvalConfig `json:"config"`
	Tasks       []string          `json:"tasks"`
	Graders     []GraderSummary   `json:"graders"`
}

type GraderSummary struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (h *HandlerContext) handleEvalGet(_ context.Context, params json.RawMessage) (any, *Error) {
	var p EvalGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, ErrInvalidParams("path is required")
	}

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		return nil, ErrEvalNotFound(p.Path)
	}

	spec, err := models.LoadEvalSpec(p.Path)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	var graders []GraderSummary
	for _, g := range spec.Graders {
		graders = append(graders, GraderSummary{
			Name: g.Identifier,
			Type: string(g.Kind),
		})
	}

	return &EvalGetResult{
		Name:        spec.Name,
		Description: spec.Description,
		SkillName:   spec.SkillName,
		Config:      spec.Config,
		Tasks:       spec.Tasks,
		Graders:     graders,
	}, nil
}

// --- eval.validate ---

type EvalValidateParams struct {
	Path string `json:"path"`
}

type EvalValidateResult struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
}

func (h *HandlerContext) handleEvalValidate(_ context.Context, params json.RawMessage) (any, *Error) {
	var p EvalValidateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, ErrInvalidParams("path is required")
	}

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		return nil, ErrEvalNotFound(p.Path)
	}

	data, err := os.ReadFile(p.Path)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	var spec models.EvalSpec
	var errs []string

	if yerr := yaml.Unmarshal(data, &spec); yerr != nil {
		errs = append(errs, fmt.Sprintf("parse error: %v", yerr))
		return &EvalValidateResult{Valid: false, Errors: errs}, nil
	}

	// Schema validation via validation package
	schemaEvalErrs, schemaTaskErrs, _ := validation.ValidateEvalFile(p.Path)
	for _, e := range schemaEvalErrs {
		errs = append(errs, fmt.Sprintf("schema: %s", e))
	}
	for file, fileErrs := range schemaTaskErrs {
		for _, e := range fileErrs {
			errs = append(errs, fmt.Sprintf("%s: %s", file, e))
		}
	}

	if verr := spec.Validate(); verr != nil {
		errs = append(errs, verr.Error())
	}

	if len(spec.Tasks) == 0 {
		errs = append(errs, "no tasks defined")
	}

	if spec.Config.EngineType == "" {
		errs = append(errs, "executor is required")
	}

	return &EvalValidateResult{
		Valid:  len(errs) == 0,
		Errors: errs,
	}, nil
}

// --- eval.run ---

type EvalRunParams struct {
	Path       string `json:"path"`
	ContextDir string `json:"context_dir,omitempty"`
}

type EvalRunResult struct {
	RunID string `json:"run_id"`
}

func (h *HandlerContext) handleEvalRun(_ context.Context, params json.RawMessage) (any, *Error) {
	var p EvalRunParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, ErrInvalidParams("path is required")
	}

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		return nil, ErrEvalNotFound(p.Path)
	}

	// Validate the spec can be loaded
	if _, err := models.LoadEvalSpec(p.Path); err != nil {
		return nil, ErrValidationFailed(err.Error())
	}

	// Create a run ID and track it
	h.mu.Lock()
	h.nextRunID++
	runID := fmt.Sprintf("run-%d", h.nextRunID)
	state := &RunState{ID: runID, Status: "running"}
	h.runs[runID] = state
	_, cancel := context.WithCancel(context.Background())
	h.cancelFuncs[runID] = cancel
	h.mu.Unlock()

	// In a full implementation, this would launch the eval asynchronously.
	// For now, we mark the run as completed to make the protocol functional.
	go func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		state.Status = "completed"
		// Clean up cancel func to avoid leaking entries for completed runs.
		if cancel, exists := h.cancelFuncs[runID]; exists {
			cancel()
			delete(h.cancelFuncs, runID)
		}
	}()

	return &EvalRunResult{RunID: runID}, nil
}

// --- task.list ---

type TaskListParams struct {
	Path string `json:"path"`
}

type TaskListResult struct {
	Tasks []TaskSummary `json:"tasks"`
}

type TaskSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *HandlerContext) handleTaskList(_ context.Context, params json.RawMessage) (any, *Error) {
	var p TaskListParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, ErrInvalidParams("path is required")
	}

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		return nil, ErrEvalNotFound(p.Path)
	}

	spec, err := models.LoadEvalSpec(p.Path)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	specDir := filepath.Dir(p.Path)
	taskFiles, err := spec.ResolveTestFiles(specDir)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	var tasks []TaskSummary
	for _, tf := range taskFiles {
		tc, loadErr := models.LoadTaskSpec(tf)
		if loadErr != nil {
			tasks = append(tasks, TaskSummary{ID: tf, Name: filepath.Base(tf)})
			continue
		}
		tasks = append(tasks, TaskSummary{ID: tc.TestID, Name: tc.DisplayName})
	}

	return &TaskListResult{Tasks: tasks}, nil
}

// --- task.get ---

type TaskGetParams struct {
	Path   string `json:"path"`
	TaskID string `json:"task_id"`
}

func (h *HandlerContext) handleTaskGet(_ context.Context, params json.RawMessage) (any, *Error) {
	var p TaskGetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.Path == "" {
		return nil, ErrInvalidParams("path is required")
	}
	if p.TaskID == "" {
		return nil, ErrInvalidParams("task_id is required")
	}

	if _, err := os.Stat(p.Path); os.IsNotExist(err) {
		return nil, ErrEvalNotFound(p.Path)
	}

	spec, err := models.LoadEvalSpec(p.Path)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	specDir := filepath.Dir(p.Path)
	taskFiles, err := spec.ResolveTestFiles(specDir)
	if err != nil {
		return nil, ErrInternalError(err.Error())
	}

	for _, tf := range taskFiles {
		tc, loadErr := models.LoadTaskSpec(tf)
		if loadErr != nil {
			continue
		}
		if tc.TestID == p.TaskID {
			return tc, nil
		}
	}

	return nil, ErrInvalidParams(fmt.Sprintf("task %q not found", p.TaskID))
}

// --- run.status ---

type RunStatusParams struct {
	RunID string `json:"run_id"`
}

func (h *HandlerContext) handleRunStatus(_ context.Context, params json.RawMessage) (any, *Error) {
	var p RunStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.RunID == "" {
		return nil, ErrInvalidParams("run_id is required")
	}

	h.mu.Lock()
	state, ok := h.runs[p.RunID]
	h.mu.Unlock()
	if !ok {
		return nil, ErrInvalidParams(fmt.Sprintf("run %q not found", p.RunID))
	}

	return state, nil
}

// --- run.cancel ---

type RunCancelParams struct {
	RunID string `json:"run_id"`
}

type RunCancelResult struct {
	Canceled bool `json:"canceled"`
}

func (h *HandlerContext) handleRunCancel(_ context.Context, params json.RawMessage) (any, *Error) {
	var p RunCancelParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if p.RunID == "" {
		return nil, ErrInvalidParams("run_id is required")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	state, ok := h.runs[p.RunID]
	if !ok {
		return nil, ErrInvalidParams(fmt.Sprintf("run %q not found", p.RunID))
	}

	if state.Status != "running" {
		return &RunCancelResult{Canceled: false}, nil
	}

	if cancel, exists := h.cancelFuncs[p.RunID]; exists {
		cancel()
		delete(h.cancelFuncs, p.RunID)
	}
	state.Status = "canceled"

	return &RunCancelResult{Canceled: true}, nil
}
