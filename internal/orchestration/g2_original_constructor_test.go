// Frozen from the approved a9928cd4e original sources; test-local symbol renames only.
package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/utils"
)

type frozenOriginalRunner struct {
	cfg          *config.EvalConfig
	requestFiles RequestFiles
}

func (r *frozenOriginalRunner) buildExecutionRequest(tc *models.TestCase) (*execution.ExecutionRequest, error) {
	// Load resource files
	r.setRequestInputRole("context_fixture")
	resources, err := r.frozenOriginalLoadContextFixtureResources(tc)
	if err != nil {
		return nil, err
	}
	r.setRequestInputRole("resource")
	resources = append(resources, r.loadResources(tc)...)
	r.setRequestInputRole("instruction")
	instructions, instructionResources, err := r.loadInstructionFiles(tc)
	if err != nil {
		return nil, err
	}
	resources = append(resources, instructionResources...)
	if err := rejectRelativePathPromptWithEmptySandbox(tc, resources); err != nil {
		return nil, err
	}

	spec := r.cfg.Spec()
	resolvedSkillPaths := r.taskSkillPaths(tc)
	noSkills := spec.SkillsDisabledForTask(tc.SkillPaths)
	_, fm, err := r.resolveTaskAgent(tc)
	if err != nil {
		return nil, err
	}

	return &execution.ExecutionRequest{
		Message:           tc.Stimulus.Message,
		Context:           tc.Stimulus.Metadata,
		Resources:         resources,
		GitResources:      tc.Stimulus.Repos,
		WorkDir:           tc.Stimulus.WorkDir,
		Instructions:      instructions,
		SkillName:         spec.SkillName,
		TaskName:          tc.DisplayName,
		TaskDescription:   tc.Summary,
		SkillPaths:        resolvedSkillPaths,
		NoSkills:          noSkills,
		SuppressSkillBody: !spec.Config.ShouldInjectSkillBody(),
		TriggerSkillRouting: spec.Config.ShouldTriggerSkillRouting() &&
			execution.IsSkillAvailable(resolvedSkillPaths, spec.SkillName),
		MCPServers:          convertMCPServers(spec.Config.ServerConfigs, spec.MCPMocks, r.cfg.SpecDir()),
		CommandMocks:        effectiveCommandMocks(tc, spec),
		CommandMocksBaseDir: r.cfg.SpecDir(),
		FirstEventTimeout:   r.firstEventTimeout(tc),
		ToolPolicy:          resolveToolPolicy(fm),
		ModelID:             spec.Config.ModelID,
		ReasoningEffort:     spec.Config.ReasoningEffort,
	}, nil
}

func (r *frozenOriginalRunner) loadResources(tc *models.TestCase) []execution.ResourceFile {
	var resources []execution.ResourceFile

	// Determine fixture directory (for loading resource files)
	fixtureDir := r.cfg.FixtureDir()
	if tc.ContextRoot != "" {
		fixtureDir = tc.ContextRoot
	}

	for _, ref := range tc.Stimulus.Resources {
		if ref.Body != "" {
			// Inline content
			resources = append(resources, execution.ResourceFile{
				Path:    ref.Location,
				Content: []byte(ref.Body),
			})
		} else if ref.Location != "" && fixtureDir != "" {
			// Load from file - validate path to prevent directory traversal
			if filepath.IsAbs(ref.Location) {
				fmt.Fprintf(os.Stderr, "Warning: absolute resource path %q rejected\n", ref.Location)
				continue
			}

			cleanPath := filepath.Clean(ref.Location)
			if strings.Contains(cleanPath, "..") {
				fmt.Fprintf(os.Stderr, "Warning: resource path %q contains '..' and is rejected\n", ref.Location)
				continue
			}

			fullPath := filepath.Join(fixtureDir, cleanPath)

			// Ensure the resolved path is still within fixtureDir
			absFixtureDir, err := filepath.Abs(fixtureDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get absolute path for fixture dir: %v\n", err)
				continue
			}

			absFullPath, err := filepath.Abs(fullPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to get absolute path for resource: %v\n", err)
				continue
			}

			if !strings.HasPrefix(absFullPath, absFixtureDir+string(filepath.Separator)) {
				fmt.Fprintf(os.Stderr, "Warning: resource path %q escapes fixture directory\n", ref.Location)
				continue
			}

			content, err := r.inputFiles().ReadFile(fullPath)
			if err != nil {
				// Log error but continue - let the test fail if resource is critical
				fmt.Fprintf(os.Stderr, "Warning: failed to load resource file %s: %v\n", fullPath, err)
				continue
			}
			resources = append(resources, execution.ResourceFile{
				Path:    ref.Location,
				Content: content,
			})
		}
	}

	return resources
}

func (r *frozenOriginalRunner) frozenOriginalLoadContextFixtureResources(tc *models.TestCase) ([]execution.ResourceFile, error) {
	return frozenOriginalLoadContextFixtureResources(tc, r.cfg.SpecDir(), r.inputFiles())
}

// LoadContextFixtureResources reads local fixtures with runtime containment and
// symlink rules. It does not create a workspace or initialize an engine.

func frozenOriginalLoadContextFixtureResources(tc *models.TestCase, baseDir string, files RequestFiles) ([]execution.ResourceFile, error) {
	fixtureValue, ok := tc.Stimulus.Metadata["fixture"]
	if !ok {
		return nil, nil
	}
	fixturePath, ok := fixtureValue.(string)
	if !ok {
		return nil, fmt.Errorf("inputs.context.fixture must be a string")
	}
	if fixturePath == "" {
		return nil, fmt.Errorf("inputs.context.fixture must not be empty")
	}
	if filepath.IsAbs(fixturePath) || strings.HasPrefix(fixturePath, "/") || strings.HasPrefix(fixturePath, "\\") || filepath.VolumeName(fixturePath) != "" {
		return nil, fmt.Errorf("inputs.context.fixture path %q must be relative", fixturePath)
	}
	if containsPathTraversal(fixturePath) {
		return nil, fmt.Errorf("inputs.context.fixture path %q must not contain path traversal", fixturePath)
	}

	if baseDir == "" {
		baseDir = "."
	}

	cleanFixturePath := filepath.Clean(fixturePath)
	if cleanFixturePath == "." {
		return nil, fmt.Errorf("inputs.context.fixture path %q must not refer to the spec directory itself", fixturePath)
	}
	fullPath := filepath.Join(baseDir, cleanFixturePath)
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory: %w", err)
	}
	absFullPath, err := filepath.Abs(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolving inputs.context.fixture path %q: %w", fixturePath, err)
	}
	if absFullPath != absBaseDir && !strings.HasPrefix(absFullPath, absBaseDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("inputs.context.fixture path %q escapes spec directory", fixturePath)
	}

	info, err := files.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("reading inputs.context.fixture %q: %w", fixturePath, err)
	}

	// Re-check containment using symlink-resolved paths.
	realFullPath, err := files.EvalSymlinks(fullPath)
	if err != nil {
		return nil, fmt.Errorf("resolving symlinks for inputs.context.fixture %q: %w", fixturePath, err)
	}
	realBaseDir, err := files.EvalSymlinks(absBaseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving symlinks for spec directory: %w", err)
	}
	if realFullPath != realBaseDir && !strings.HasPrefix(realFullPath, realBaseDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("inputs.context.fixture path %q escapes spec directory", fixturePath)
	}

	if !info.IsDir() {
		content, err := files.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("reading inputs.context.fixture file %q: %w", fixturePath, err)
		}
		return []execution.ResourceFile{{
			Path:    filepath.ToSlash(filepath.Base(cleanFixturePath)),
			Content: content,
		}}, nil
	}

	var resources []execution.ResourceFile
	err = files.WalkDir(fullPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(fullPath, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		content, err := files.ReadFile(path)
		if err != nil {
			return err
		}
		resources = append(resources, execution.ResourceFile{
			Path:    filepath.ToSlash(rel),
			Content: content,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("loading inputs.context.fixture %q: %w", fixturePath, err)
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Path < resources[j].Path
	})
	return resources, nil
}

func (r *frozenOriginalRunner) loadInstructionFiles(tc *models.TestCase) ([]execution.InstructionFile, []execution.ResourceFile, error) {
	spec := r.cfg.Spec()
	paths := append([]string{}, spec.Config.InstructionFiles...)
	paths = append(paths, tc.InstructionFiles...)
	if len(paths) == 0 {
		return nil, nil, nil
	}

	fixtureDir := r.cfg.FixtureDir()
	if tc.ContextRoot != "" {
		fixtureDir = tc.ContextRoot
	}
	if fixtureDir == "" {
		return nil, nil, fmt.Errorf("instruction_files require a context/fixtures directory")
	}

	instructions := make([]execution.InstructionFile, 0, len(paths))
	resources := make([]execution.ResourceFile, 0, len(paths))
	for _, path := range paths {
		cleanPath, fullPath, err := resolveContextFile(fixtureDir, path, "instruction_files")
		if err != nil {
			return nil, nil, err
		}

		content, err := r.inputFiles().ReadFile(fullPath)
		if err != nil {
			return nil, nil, fmt.Errorf("reading instruction file %q: %w", path, err)
		}

		instructions = append(instructions, execution.InstructionFile{
			Path:    filepath.ToSlash(cleanPath),
			Content: content,
		})
		resources = append(resources, execution.ResourceFile{
			Path:    filepath.ToSlash(cleanPath),
			Content: content,
		})
	}

	return instructions, resources, nil
}

func (r *frozenOriginalRunner) taskSkillPaths(tc *models.TestCase) []string {
	skillPaths := r.cfg.Spec().Config.FilteredSkillPaths()
	if len(tc.SkillPaths) > 0 || (r.cfg.Spec().Scenario != "" && tc.SkillPaths != nil) {
		skillPaths = tc.SkillPaths
	}
	return utils.ResolvePaths(skillPaths, r.cfg.SpecDir())
}

func (r *frozenOriginalRunner) resolveTaskAgent(tc *models.TestCase) (string, *skill.AgentFrontmatter, error) {
	if r.cfg.Spec().SkillsDisabledForTask(tc.SkillPaths) {
		return "", nil, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("resolving agent working directory: %w", err)
	}
	return execution.ResolveAgentDefinition(append([]string{cwd}, r.taskSkillPaths(tc)...), r.cfg.Spec().SkillName)
}

func (r *frozenOriginalRunner) firstEventTimeout(tc *models.TestCase) time.Duration {
	sec := r.cfg.Spec().Config.FirstEventTimeoutSec
	if tc.FirstEventTimeoutSec != nil {
		sec = *tc.FirstEventTimeoutSec
	}
	if sec <= 0 {
		return 0
	}
	return time.Duration(sec) * time.Second
}

func (r *frozenOriginalRunner) inputFiles() RequestFiles {
	if r.requestFiles != nil {
		return r.requestFiles
	}
	return nativeRequestFiles{}
}

func (r *frozenOriginalRunner) setRequestInputRole(role string) {
	if files, ok := r.requestFiles.(interface{ SetInputRole(string) }); ok {
		files.SetInputRole(role)
	}
}

// ValidateNativeSnapshotConfiguration is declaration support, not runtime
// admission. It neither constructs an engine nor assesses ambient instructions.

func frozenOriginalBuildCapturedNativeRequest(spec *models.EvalSpec, tc *models.TestCase, specDir, contextDir string, files RequestFiles) (*execution.ExecutionRequest, error) {
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
	r := &frozenOriginalRunner{cfg: config.NewEvalConfig(spec, config.WithSpecDir(specDir), config.WithFixtureDir(contextDir)), requestFiles: files}
	req, err := r.buildExecutionRequest(tc)
	if err != nil {
		return nil, err
	}
	req.ToolPolicy = &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll}
	req.SkipWorkspaceCapture = true
	req.CommandMocksBaseDir = ""
	return req, nil
}
