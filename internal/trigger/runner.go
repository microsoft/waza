package trigger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/copilotconfig"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/safeio"
	"github.com/microsoft/waza/internal/transcript"
	"github.com/microsoft/waza/internal/utils"
)

// Runner executes trigger tests and returns classification metrics.
type Runner struct {
	spec       *TestSpec
	engine     execution.AgentEngine
	cfg        *config.EvalConfig
	out        io.Writer
	fixtures   []execution.ResourceFile // cached fixture files, loaded once
	fixtureErr error
	mcpConfig  map[string]copilot.MCPServerConfig
}

type task struct {
	prompt        string
	confidence    string
	shouldTrigger bool
}

type taskResult struct {
	triggered  bool
	response   string
	transcript []models.TranscriptEvent
	toolCalls  []models.ToolCall
	sessionID  string
	err        error
}

func NewRunner(spec *TestSpec, engine execution.AgentEngine, cfg *config.EvalConfig, out io.Writer) *Runner {
	r := &Runner{spec: spec, engine: engine, cfg: cfg, out: out}
	r.fixtures, r.fixtureErr = loadFixtureDir(cfg.FixtureDir())
	r.mcpConfig = convertMCPServers(cfg.Spec().Config.ServerConfigs, cfg.Spec().MCPMocks, cfg.SpecDir())
	return r
}

func (r *Runner) Run(ctx context.Context) (*models.TriggerMetrics, error) {
	_, m, err := r.RunDetailed(ctx)
	return m, err
}

func (r *Runner) RunDetailed(ctx context.Context) ([]models.TriggerResult, *models.TriggerMetrics, error) {
	if r.fixtureErr != nil {
		return nil, nil, fmt.Errorf("loading trigger fixtures: %w", r.fixtureErr)
	}

	var tasks []task
	for _, p := range r.spec.ShouldTriggerPrompts {
		tasks = append(tasks, task{prompt: p.Prompt, confidence: p.Confidence, shouldTrigger: true})
	}
	for _, p := range r.spec.ShouldNotTriggerPrompts {
		tasks = append(tasks, task{prompt: p.Prompt, confidence: p.Confidence, shouldTrigger: false})
	}

	workers := r.cfg.Spec().Config.Workers
	// Auto-size workers (#135 R3): default to min(NumCPU, jobs, cap=8) when
	// unset, clamp to job count when over-requested, and log if capped.
	workers = orchestration.ResolveWorkers(workers, len(tasks), "trigger tests", r.out)
	outcomes := make([]taskResult, len(tasks))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, t := range tasks {
		wg.Add(1)
		go func(i int, t task) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			resp, err := r.testTrigger(ctx, t.prompt)
			if err != nil {
				outcomes[i] = taskResult{err: err}
				return
			}
			triggered := slices.ContainsFunc(resp.SkillInvocations, func(si execution.SkillInvocation) bool {
				return si.Name == r.spec.Skill
			})
			outcomes[i] = taskResult{
				triggered:  triggered,
				response:   resp.FinalOutput,
				transcript: transcript.BuildFromSessionEvents(copilotevents.ToSDK(resp.Events)),
				toolCalls:  resp.ToolCalls,
				sessionID:  resp.SessionID,
			}
		}(i, t)
	}
	wg.Wait()

	var results []models.TriggerResult
	var errorCount int
	for i, t := range tasks {
		o := outcomes[i]
		if o.err != nil {
			errorCount++
			if r.cfg.Verbose() && r.out != nil {
				if _, err := fmt.Fprintf(r.out, "    ✗ [error] %q: %v\n", t.prompt, o.err); err != nil {
					fmt.Fprintf(os.Stderr, "error writing trigger test output: %v\n", err)
				}
			}
			results = append(results, models.TriggerResult{
				Prompt:        t.prompt,
				Confidence:    t.confidence,
				ShouldTrigger: t.shouldTrigger,
				DidTrigger:    !t.shouldTrigger,
				ErrorMsg:      o.err.Error(),
			})
			continue
		}

		correct := t.shouldTrigger == o.triggered
		icon := "✓"
		if !correct {
			icon = "✗"
		}

		if r.cfg.Verbose() && r.out != nil {
			label := "should trigger"
			if !t.shouldTrigger {
				label = "should NOT trigger"
			}
			conf := t.confidence
			if conf == "" {
				conf = "high"
			}
			if _, err := fmt.Fprintf(r.out, "    %s [%s, %s] %q\n", icon, label, conf, t.prompt); err != nil {
				fmt.Fprintf(os.Stderr, "error writing trigger test output: %v\n", err)
			}
			if !correct {
				if _, err := fmt.Fprintf(r.out, "      Response: %s\n", o.response); err != nil {
					fmt.Fprintf(os.Stderr, "error writing trigger test output: %v\n", err)
				}
			}
		}

		results = append(results, models.TriggerResult{
			Prompt:        t.prompt,
			Confidence:    t.confidence,
			DidTrigger:    o.triggered,
			ShouldTrigger: t.shouldTrigger,
			FinalOutput:   o.response,
			Transcript:    o.transcript,
			ToolCalls:     o.toolCalls,
			SessionID:     o.sessionID,
		})
	}

	m := models.ComputeTriggerMetrics(results)
	if m == nil {
		return nil, nil, fmt.Errorf("no trigger test results collected")
	}
	m.Errors = errorCount
	return results, m, nil
}

func (r *Runner) testTrigger(ctx context.Context, prompt string) (*execution.ExecutionResponse, error) {
	spec := r.cfg.Spec()
	timeout := spec.Config.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	specDir := r.cfg.SpecDir()
	filteredSkillPaths := spec.Config.FilteredSkillPaths()
	skillPaths := utils.ResolvePaths(filteredSkillPaths, specDir)
	specDirBlocked := utils.IsFilteredPath(specDir, spec.Config.SkillPaths, filteredSkillPaths, specDir)
	effectiveSkillDirs := append([]string(nil), skillPaths...)
	if specDir != "" && !specDirBlocked {
		effectiveSkillDirs = append([]string{specDir}, effectiveSkillDirs...)
	}
	requestSkillPaths := skillPaths
	if spec.Config.Sandbox != nil && spec.Config.Sandbox.Enabled && specDir != "" && !specDirBlocked {
		requestSkillPaths = append([]string{specDir}, skillPaths...)
	}
	return r.engine.Execute(execCtx, &execution.ExecutionRequest{
		Message:           prompt,
		SkillName:         r.spec.Skill,
		RequiredSkills:    append([]string(nil), spec.Config.RequiredSkills...),
		SkillPaths:        requestSkillPaths,
		NoSkills:          spec.Config.AllSkillsDisabled(),
		SuppressSkillBody: !spec.Config.ShouldInjectSkillBody(),
		TriggerSkillRouting: spec.Config.ShouldTriggerSkillRouting() &&
			!spec.Config.AllSkillsDisabled() &&
			execution.IsSkillAvailable(effectiveSkillDirs, r.spec.Skill),
		SourceDir:               r.cfg.SpecDir(),
		Resources:               r.fixtures,
		MCPServers:              r.mcpConfig,
		Sandbox:                 spec.Config.Sandbox,
		CancelOnSkillInvocation: true,
	})
}

// loadFixtureDir recursively walks a fixture directory and returns all files
// as ResourceFiles. Skips hidden dirs, node_modules, vendor, and binary files.
// Returns nil if dir is empty or doesn't exist.
func loadFixtureDir(dir string) ([]execution.ResourceFile, error) {
	if dir == "" {
		return nil, nil
	}

	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking fixture directory: %w", err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	var resources []execution.ResourceFile
	root, err := safeio.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening fixture directory: %w", err)
	}

	walkErr := fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		name := d.Name()
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		// Skip hidden directories, node_modules, vendor
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip large files (>1MB) to prevent bloating workspace
		content, _, err := root.ReadRegularFile(path, 1<<20)
		if errors.Is(err, safeio.ErrFileTooLarge) {
			return nil
		}
		if err != nil {
			return err
		}

		resources = append(resources, execution.ResourceFile{
			Path:    filepath.FromSlash(path),
			Content: content,
		})
		return nil
	})
	closeErr := root.Close()
	if walkErr != nil {
		return nil, fmt.Errorf("walking fixture directory: %w", walkErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing fixture directory: %w", closeErr)
	}

	return resources, nil
}

// convertMCPServers converts the eval YAML mcp_servers config (map[string]any)
// into the copilot SDK's MCPServerConfig type.
func convertMCPServers(serverConfigs map[string]any, mocks []models.MCPMockConfig, baseDir string) map[string]copilot.MCPServerConfig {
	return copilotconfig.ConvertMCPServersWithMocks(serverConfigs, mocks, baseDir, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format, args...)
	})
}
