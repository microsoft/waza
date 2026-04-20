package trigger

import (
	"context"
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
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/transcript"
	"github.com/microsoft/waza/internal/utils"
)

// Runner executes trigger tests and returns classification metrics.
type Runner struct {
	spec      *TestSpec
	engine    execution.AgentEngine
	cfg       *config.BenchmarkConfig
	out       io.Writer
	fixtures  []execution.ResourceFile // cached fixture files, loaded once
	mcpConfig map[string]copilot.MCPServerConfig
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

func NewRunner(spec *TestSpec, engine execution.AgentEngine, cfg *config.BenchmarkConfig, out io.Writer) *Runner {
	r := &Runner{spec: spec, engine: engine, cfg: cfg, out: out}
	r.fixtures = loadFixtureDir(cfg.FixtureDir())
	r.mcpConfig = convertMCPServers(cfg.Spec().Config.ServerConfigs)
	return r
}

func (r *Runner) Run(ctx context.Context) (*models.TriggerMetrics, error) {
	_, m, err := r.RunDetailed(ctx)
	return m, err
}

func (r *Runner) RunDetailed(ctx context.Context) ([]models.TriggerResult, *models.TriggerMetrics, error) {
	var tasks []task
	for _, p := range r.spec.ShouldTriggerPrompts {
		tasks = append(tasks, task{prompt: p.Prompt, confidence: p.Confidence, shouldTrigger: true})
	}
	for _, p := range r.spec.ShouldNotTriggerPrompts {
		tasks = append(tasks, task{prompt: p.Prompt, confidence: p.Confidence, shouldTrigger: false})
	}

	workers := r.cfg.Spec().Config.Workers
	if workers <= 0 {
		workers = 4
	}
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
				transcript: transcript.BuildFromSessionEvents(resp.Events),
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
	return r.engine.Execute(ctx, &execution.ExecutionRequest{
		Message:    prompt,
		SkillName:  r.spec.Skill,
		SkillPaths: utils.ResolvePaths(spec.Config.SkillPaths, r.cfg.SpecDir()),
		SourceDir:  r.cfg.SpecDir(),
		Resources:  r.fixtures,
		MCPServers: r.mcpConfig,
		Timeout:    time.Duration(timeout) * time.Second,
	})
}

// loadFixtureDir recursively walks a fixture directory and returns all files
// as ResourceFiles. Skips hidden dirs, node_modules, vendor, and binary files.
// Returns nil if dir is empty or doesn't exist.
func loadFixtureDir(dir string) []execution.ResourceFile {
	if dir == "" {
		return nil
	}

	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

	var resources []execution.ResourceFile
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}

	_ = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}

		name := d.Name()

		// Skip hidden directories, node_modules, vendor
		if d.IsDir() {
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip large files (>1MB) to prevent bloating workspace
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		relPath, err := filepath.Rel(absDir, path)
		if err != nil {
			return nil
		}

		resources = append(resources, execution.ResourceFile{
			Path:    relPath,
			Content: content,
		})
		return nil
	})

	return resources
}

// convertMCPServers converts the eval YAML mcp_servers config (map[string]any)
// into the copilot SDK's MCPServerConfig type.
func convertMCPServers(serverConfigs map[string]any) map[string]copilot.MCPServerConfig {
	if len(serverConfigs) == 0 {
		return nil
	}

	result := make(map[string]copilot.MCPServerConfig, len(serverConfigs))
	for name, cfg := range serverConfigs {
		cfgMap, ok := cfg.(map[string]any)
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: mcp_server %q config is not a map, skipping\n", name)
			continue
		}
		result[name] = copilot.MCPServerConfig(cfgMap)
	}
	return result
}
