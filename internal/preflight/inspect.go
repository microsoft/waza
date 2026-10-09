package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/dataset"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/registry"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/utils"
	"github.com/microsoft/waza/internal/validation"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Options struct {
	// ContextDir follows run's CLI resolution: relative to the process cwd.
	ContextDir string
}

// Inspect performs local reads only. It does not construct an engine or grader,
// run hooks, start mocks, invoke subprocesses, or resolve remote dependencies.
// A report survives invalid configuration; Complete tracks examination, not validity.
func Inspect(evalPath string, opts Options) *Report {
	r := &Report{
		Kind: ReportKind, SchemaVersion: "1.0", Source: evalPath, Complete: true,
		Tasks: []TaskPlan{}, Capabilities: []Capability{}, Dependencies: []Dependency{}, Diagnostics: []Diagnostic{},
	}
	data, err := os.ReadFile(evalPath)
	if err != nil {
		r.add("eval.read", Invalid, evalPath, "", "", "Eval file is not readable.", "Supply an existing readable eval YAML file.")
		r.Complete = false
		return r
	}
	for range validation.ValidateEvalBytes(data) {
		r.add("eval.schema", Invalid, evalPath, "", "", "Eval configuration does not match the embedded schema.", "Check required fields, types, and configuration against schemas/eval.schema.json.")
	}
	spec, err := models.LoadEvalSpecOffline(evalPath)
	if err != nil {
		r.add("eval.configuration", schemaState(err), evalPath, "", "", "Eval could not be decoded or validated offline.", "Check schemaVersion, YAML types, self-contained grader schemas, trials, timeout, and executor-specific settings.")
		r.Complete = false
		return r
	}
	r.Executor = spec.Config.EngineType
	base, err := filepath.Abs(filepath.Dir(evalPath))
	if err != nil {
		r.add("eval.path", Invalid, evalPath, "", "", "Eval directory cannot be resolved.", "Use a valid local eval path.")
		r.Complete = false
		return r
	}
	contextDir := filepath.Join(base, "fixtures")
	if opts.ContextDir != "" {
		contextDir, err = filepath.Abs(opts.ContextDir)
		if err != nil {
			r.add("context.path", Invalid, evalPath, "", "", "Context directory cannot be resolved.", "Supply an existing local context directory.")
			r.Complete = false
			return r
		}
	}
	inspectLockedGraders(r, spec, evalPath)
	applyWorkspaceSkillDefaults(r, spec)
	if err := orchestration.ValidateRequiredSkillConfiguration(spec, base); err != nil {
		r.add("skill.required", Invalid, evalPath, "", "", "Required skills are missing or cannot be parsed in configured eval-level directories.", "Configure readable skill_directories containing every required_skills declaration; runtime discovery precedence is preserved.")
	}
	inspectCapabilities(r, spec)
	inspectMCP(r, spec, base)
	inspectGraders(r, spec.Graders, evalPath, "", contextDir)
	tasks, sources := discoverTasks(r, spec, base)
	ids := map[string]bool{}
	for i, task := range tasks {
		source := sources[i]
		if strings.TrimSpace(task.TestID) == "" || ids[task.TestID] {
			r.add("task.id", Invalid, source, task.TestID, "", "Task ID is empty or duplicated.", "Assign a unique nonempty ID across all discovered tasks, including disabled tasks.")
		}
		ids[task.TestID] = true
		fixtures := contextDir
		if task.ContextRoot != "" {
			// The existing runner uses task context_dir as supplied, relative to cwd.
			fixtures, err = filepath.Abs(task.ContextRoot)
			if err != nil {
				r.add("context.path", Invalid, source, task.TestID, "", "Task context directory cannot be resolved.", "Use a valid context_dir path.")
				r.Complete = false
			}
		}
		plan := TaskPlan{
			ID: task.TestID, Name: task.DisplayName, Source: source,
			Enabled: task.Active == nil || *task.Active, ContextDir: fixtures,
			Requirements: resolveRequirements(r, task, spec, source),
		}
		r.Tasks = append(r.Tasks, plan)
		inspectTask(r, task, spec, source, base, fixtures)
	}
	if len(tasks) == 0 && r.Complete {
		r.add("tasks.empty", Invalid, evalPath, "", "", "No tasks were discovered.", "Supply matching task files or a nonempty selected CSV range.")
	}
	sort.SliceStable(r.Diagnostics, func(i, j int) bool {
		a, b := r.Diagnostics[i], r.Diagnostics[j]
		return strings.Join([]string{a.Source, a.TaskID, a.RequirementID, a.Code, a.Message}, "\x00") <
			strings.Join([]string{b.Source, b.TaskID, b.RequirementID, b.Code, b.Message}, "\x00")
	})
	return r
}

func discoverTasks(r *Report, spec *models.EvalSpec, base string) ([]*models.TestCase, []string) {
	if spec.TasksFrom != "" {
		tasks, err := dataset.LoadTasks(spec, base, time.Unix(0, 0).UTC())
		if err != nil {
			r.add("tasks.csv", Invalid, r.Source, "", "", "CSV dataset could not be selected or rendered.", "Check tasks_from containment, CSV headers/rows, range bounds, and template variables.")
			r.Complete = false
			return nil, nil
		}
		sources := make([]string, len(tasks))
		for i := range sources {
			sources[i] = filepath.Join(base, spec.TasksFrom)
		}
		return tasks, sources
	}
	var tasks []*models.TestCase
	var sources []string
	for _, pattern := range spec.Tasks {
		matches, err := filepath.Glob(filepath.Join(base, pattern))
		if err != nil || len(matches) == 0 {
			r.add("tasks.pattern", Invalid, r.Source, "", "", "A task pattern is invalid or has no matches.", "Ensure every tasks entry is a valid glob matching readable YAML files.")
			r.Complete = false
			continue
		}
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				r.add("task.read", Invalid, path, "", "", "Task file is not readable.", "Use readable YAML task files, not directories.")
				r.Complete = false
				continue
			}
			for range validation.ValidateTaskBytes(data) {
				r.add("task.schema", Invalid, path, "", "", "Task does not match the embedded schema.", "Check id, name, inputs, grader configuration, and requirement metadata against schemas/task.schema.json.")
			}
			task, err := models.LoadTestCaseOffline(path)
			if err != nil {
				r.add("task.configuration", schemaState(err), path, "", "", "Task could not be decoded or validated offline.", "Check prompt/prompt_file, paths, self-contained grader schemas, checkpoints, responder settings, and mocks.")
				r.Complete = false
				continue
			}
			if err := task.ValidateForExecutor(spec.Config.EngineType); err != nil {
				r.add("task.executor", Invalid, path, task.TestID, "", "Task settings are incompatible with the configured executor.", "Use copilot-sdk for command mocks and explicit judge reasoning effort.")
			}
			if task.CommandMocks != nil {
				if err := models.ValidateCommandMocksSchemaVersion(spec.SchemaVersion); err != nil {
					r.add("task.version", Invalid, path, task.TestID, "", "Task command mocks require a supported enclosing schemaVersion.", "Select schemaVersion 1.3 or newer for legacy command mocks.")
				}
			}
			tasks = append(tasks, task)
			sources = append(sources, path)
		}
	}
	return tasks, sources
}

func inspectCapabilities(r *Report, spec *models.EvalSpec) {
	state := Verified
	meaning := "Static implementation supports task execution; runtime availability is not assessed."
	switch spec.Config.EngineType {
	case "copilot-sdk":
		r.add("runtime.availability", Unresolved, r.Source, "", "",
			"Model, CLI/SDK installation, authentication, permissions, and live resulting state are not checked offline.",
			"Verify runtime readiness explicitly outside preflight; do not treat a success message as external-state evidence.")
	case "mock":
		meaning = "Deterministic harness only; not evidence of agent quality or live side effects."
	default:
		state = Unsupported
		meaning = "Executor is not implemented by this reader."
		r.add("runtime.executor", Unsupported, r.Source, "", "", meaning, "Select an implemented executor or a reader that explicitly supports this backend.")
	}
	r.Capabilities = append(r.Capabilities, Capability{Name: "task_execution", State: state, Meaning: meaning})
	for _, name := range []string{"mcp_servers", "command_mocks", "runtime_tool_boundary"} {
		support := Unsupported
		if spec.Config.EngineType == "copilot-sdk" {
			support = Verified
		}
		r.Capabilities = append(r.Capabilities, Capability{Name: name, State: support,
			Meaning: "Static capability only; not proof of configuration enforcement or task success."})
	}
	if spec.Config.EngineType != "copilot-sdk" && (len(spec.MCPMocks) > 0 || len(spec.Config.ServerConfigs) > 0) {
		r.add("runtime.mcp", Unsupported, r.Source, "", "", "Configured MCP dependencies require an executor with MCP support.", "Use copilot-sdk for MCP execution.")
	}
	if len(spec.Hooks.BeforeRun)+len(spec.Hooks.AfterRun)+len(spec.Hooks.BeforeTask)+len(spec.Hooks.AfterTask) > 0 {
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "hooks", Name: "lifecycle", Mode: "subprocess", State: Unresolved})
		r.add("hooks.execution", Unresolved, r.Source, "", "", "Lifecycle hooks were not executed.", "Review hook executables and their effects before running the eval.")
	}
}

func inspectMCP(r *Report, spec *models.EvalSpec, base string) {
	mocked := map[string]bool{}
	for _, mock := range spec.MCPMocks {
		mocked[strings.TrimSpace(mock.Name)] = true
		state := Verified
		if cfg, err := mcpmock.FromEvalConfigOffline(mock, base); err != nil {
			state = schemaState(err)
			r.add("mock.mcp", state, r.Source, "", "", "MCP mock fixtures or matching configuration could not be verified.", "Check fixture files, tool responses, regexes, and self-contained match_schema references.")
		} else {
			for _, tool := range cfg.Tools {
				if doc, ok := tool.InputSchema.(map[string]any); ok {
					if err := compileOfflineSchema(doc, filepath.Join(base, "mock-input-schema.json")); err != nil {
						state = schemaState(err)
						r.add("mock.input_schema", state, r.Source, "", "", "Mock tool input schema could not be verified offline.", "Use valid self-contained JSON schemas.")
					}
				}
			}
		}
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "mcp", Name: mock.Name, Mode: "mocked", State: state})
		if _, exists := spec.Config.ServerConfigs[strings.TrimSpace(mock.Name)]; exists {
			r.add("mock.mcp_override", Verified, r.Source, "", "", "MCP mock replaces the same-named live server configuration.", "Mock output checks the harness, not live service resulting state.")
		}
	}
	names := make([]string, 0, len(spec.Config.ServerConfigs))
	for name := range spec.Config.ServerConfigs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if mocked[name] {
			continue
		}
		state := Unresolved
		config, ok := spec.Config.ServerConfigs[name].(map[string]any)
		if !ok {
			state = Invalid
		} else {
			serverType, _ := config["type"].(string)
			switch strings.ToLower(serverType) {
			case "", "stdio":
				command, ok := config["command"].(string)
				if !ok || strings.TrimSpace(command) == "" {
					state = Invalid
				}
			case "http", "sse":
				url, ok := config["url"].(string)
				if !ok || strings.TrimSpace(url) == "" {
					state = Invalid
				}
			default:
				state = Unsupported
			}
		}
		if state != Unresolved {
			r.add("dependency.mcp_configuration", state, r.Source, "", "", "Live MCP configuration is malformed or its transport is unsupported.", "Use a stdio command or http/sse URL with schema-valid configuration.")
		}
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "mcp", Name: name, Mode: "live", State: state})
		r.add("dependency.live", Unresolved, r.Source, "", "", "A live MCP dependency is declared; its availability, credentials, and effects are not verified.", "Validate live dependencies explicitly outside preflight.")
	}
}

func inspectLockedGraders(r *Report, spec *models.EvalSpec, path string) {
	refs := registry.CollectGraderRefs(spec)
	if len(refs) == 0 {
		return
	}
	lock, err := models.LoadLockfile(filepath.Join(filepath.Dir(path), models.LockfileName))
	if err != nil {
		r.add("grader.lock", Invalid, path, "", "", "Remote graders require a readable valid waza.lock.", "Run waza get explicitly while online to create immutable pins; preflight never fetches.")
		r.Complete = false
		return
	}
	resolver, err := registry.NewResolver()
	if err != nil {
		r.add("grader.cache", Unresolved, path, "", "", "Local module cache cannot be located.", "Configure WAZA_MODULE_CACHE with a readable local cache.")
		r.Complete = false
		return
	}
	for i, grader := range spec.Graders {
		if grader.Ref == "" {
			continue
		}
		state := Verified
		entry, ok := lock.Grader(grader.Ref)
		if !ok {
			state = Invalid
			r.add("grader.lock_entry", Invalid, path, "", "", "A remote grader has no matching lock entry.", "Run waza get explicitly to lock every remote grader reference.")
		} else {
			ref, parseErr := registry.ParseRef(grader.Ref)
			if parseErr != nil {
				state = Invalid
				r.add("grader.ref", Invalid, path, "", "", "Remote grader reference is malformed.", "Use a supported immutable module/export reference.")
			} else {
				preset, loadErr := resolver.LoadLockedGraderOffline(context.Background(), ref, entry)
				if loadErr != nil {
					state = schemaState(loadErr)
					code := "grader.cache_integrity"
					var unavailable *registry.OfflineCacheUnavailableError
					if errors.As(loadErr, &unavailable) {
						state, code = Unresolved, "grader.cache_missing"
					}
					r.add(code, state, path, "", "", "Locked grader is unavailable, untrusted, or inconsistent with its local cache.", "Populate the pinned cache explicitly while online; verify the lock digest and trust remote program graders explicitly.")
					r.Complete = false
				} else {
					merged, mergeErr := registry.MergeGraderConfigOffline(preset, grader)
					if mergeErr != nil {
						r.Complete = false
						state = schemaState(mergeErr)
						r.add("grader.merge", state, path, "", "", "Remote preset and local grader configuration conflict or need unavailable schemas.", "Use compatible type/configuration overrides and self-contained schemas for the locked preset.")
					} else {
						spec.Graders[i] = merged
					}
				}
			}
		}
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "grader", Name: grader.Identifier, Mode: "locked-cache", State: state})
	}
}

func inspectTask(r *Report, task *models.TestCase, spec *models.EvalSpec, source, base, fixtures string) {
	for _, ref := range task.Stimulus.Resources {
		if !safeWorkspacePath(ref.Location) {
			r.add("resource.path", Invalid, source, task.TestID, "", "Resource destination is not a bounded relative path.", "Use a workspace-relative path without traversal segments.")
		} else if ref.Body == "" {
			checkFile(r, filepath.Join(fixtures, ref.Location), source, task.TestID, "resource.file")
		}
	}
	for _, path := range append(append([]string{}, spec.Config.InstructionFiles...), task.InstructionFiles...) {
		if !safeWorkspacePath(path) {
			r.add("instruction.path", Invalid, source, task.TestID, "", "Instruction path is not a bounded relative path.", "Use an instruction file within the context directory.")
		} else {
			checkFile(r, filepath.Join(fixtures, path), source, task.TestID, "instruction.file")
		}
	}
	if _, err := orchestration.LoadContextFixtureResources(task, base); err != nil {
		r.add("context.fixture", Invalid, source, task.TestID, "", "inputs.context.fixture cannot be loaded within the eval directory.", "Use a readable relative fixture file or directory; resolved symlinks must remain within the eval directory.")
	}
	if task.ContextRoot != "" {
		checkDirectory(r, fixtures, source, task.TestID, "context.directory")
	}
	paths := spec.Config.FilteredSkillPaths()
	if len(task.SkillPaths) > 0 {
		paths = task.SkillPaths
	}
	if spec.SkillsDisabledForTask(task.SkillPaths) {
		paths = nil
		r.add("skills.discovery_disabled", Verified, source, task.TestID, "",
			"Resolved configuration requests disabled skill discovery; execution uses native SDK EnableSkills=false.",
			"Static intent only; preflight does not initialize a session or verify runtime discovery.")
	}
	paths = utils.ResolvePaths(paths, base)
	for _, path := range paths {
		checkDirectory(r, path, source, task.TestID, "skill.directory")
	}
	inspectBoundaryPolicy(r, task, spec, source, paths)
	for _, repo := range task.Stimulus.Repos {
		path := repo.Source
		checkDirectory(r, path, source, task.TestID, "repository.source")
		r.add("repository.revision", Unresolved, source, task.TestID, "", "Git revision and worktree materialization were not checked; no git process was started.", "Verify the source repository and selected commit before execution.")
	}
	mocks := spec.CommandMocks
	if task.CommandMocks != nil {
		mocks = *task.CommandMocks
	}
	for _, mock := range mocks {
		start := len(r.Diagnostics)
		for _, response := range mock.Responses {
			if response.Fixture != "" {
				checkFile(r, filepath.Join(base, response.Fixture), source, task.TestID, "mock.command_fixture")
			}
		}
		state := Verified
		if len(r.Diagnostics) > start {
			state = Invalid
		}
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "command", Name: mock.Name, Mode: "mocked", State: state, TaskID: task.TestID})
	}
	if task.Stimulus.Responder != nil {
		r.Dependencies = append(r.Dependencies, Dependency{Kind: "responder", Name: "model", Mode: "model", State: Unresolved, TaskID: task.TestID})
		r.add("responder.model", Unresolved, source, task.TestID, "", "Responder model availability is not checked offline.", "Verify the responder model outside preflight.")
	}
	if len(task.Checkpoints) > 0 {
		r.add("checkpoint.scheduling", Unresolved, source, task.TestID, "", "Checkpoint execution is conditional on reaching its turn; responder termination or an earlier stop may skip it.", "Inspect reached-turn/checkpoint evidence after execution.")
	}
	graders := make([]models.GraderConfig, 0, len(task.Validators))
	finalNames := map[string]bool{}
	for _, g := range spec.Graders {
		finalNames[g.Identifier] = true
	}
	for _, g := range task.Validators {
		if finalNames[g.Identifier] {
			r.add("grader.result_collision", Unresolved, source, task.TestID, "", "Final grader names collide; existing results are keyed by name and may overwrite earlier results.", "Use distinct names before joining runtime evidence to scoped declarations.")
		}
		finalNames[g.Identifier] = true
		graders = append(graders, models.GraderConfig{Identifier: g.Identifier, Kind: g.Kind, Parameters: g.Parameters})
	}
	inspectGraders(r, graders, source, task.TestID, fixtures)
	for _, checkpoint := range task.Checkpoints {
		var checks []models.GraderConfig
		for _, g := range checkpoint.Graders {
			checks = append(checks, models.GraderConfig{Identifier: g.Identifier, Kind: g.Kind, Parameters: g.Parameters})
		}
		inspectGraders(r, checks, source, task.TestID, fixtures)
	}
}

func safeWorkspacePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, `\`) || strings.Contains(path, ":") {
		return false
	}
	for _, segment := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return false
		}
	}
	return true
}

func checkFile(r *Report, path, source, task, code string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		r.add(code, Invalid, source, task, "", "A required local file is missing or is not a regular file.", "Check the referenced path and its resolution base; preflight does not create fixtures.")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		r.add(code, Invalid, source, task, "", "A required local file is not readable.", "Grant read access to the referenced local file.")
		return
	}
	if err := file.Close(); err != nil {
		r.add(code, Invalid, source, task, "", "A required local file could not be closed after inspection.", "Check filesystem access and retry.")
	}
}

func checkDirectory(r *Report, path, source, task, code string) {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		r.add(code, Invalid, source, task, "", "A required local directory is missing or is not a directory.", "Check the configured path and its resolution base.")
	}
}

// Keep errors classifiable without exposing arbitrary configuration values.
func schemaState(err error) State {
	var loadError *jsonschema.LoadURLError
	if errors.Is(err, schemaloader.ErrExternalReference) || errors.As(err, &loadError) {
		return Unresolved
	}
	return Invalid
}
