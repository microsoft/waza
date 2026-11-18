package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/cache"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// taskCacheKey fingerprints the declarations and the consumed input bytes of
// one ordinary task. An error means the task must not use the cache.
func (r *EvalRunner) taskCacheKey(spec *models.EvalSpec, tc *models.TestCase) (string, error) {
	inputs, err := r.consumedCacheInputs(tc)
	if err != nil {
		return "", err
	}
	fixtureDir := r.cfg.FixtureDir()
	if tc.ContextRoot != "" {
		fixtureDir = tc.ContextRoot
	}
	return cache.CacheKeyWithInputs(spec, tc, fixtureDir, r.cfg.SpecDir(), inputs)
}

// consumedCacheInputs reuses the request loaders and skill resolver so the key
// covers the same bytes request construction reads.
func (r *EvalRunner) consumedCacheInputs(tc *models.TestCase) (*cache.ConsumedInputs, error) {
	inputs := &cache.ConsumedInputs{}
	fixtures, err := r.loadContextFixtureResources(tc)
	if err != nil {
		return nil, err
	}
	for _, f := range fixtures {
		inputs.Files = append(inputs.Files, cache.ConsumedFile{Role: "context_fixture", Path: f.Path, Content: f.Content})
	}
	instructions, _, err := r.loadInstructionFiles(tc)
	if err != nil {
		return nil, err
	}
	for _, f := range instructions {
		inputs.Files = append(inputs.Files, cache.ConsumedFile{Role: "instruction", Path: f.Path, Content: f.Content})
	}

	spec := r.cfg.Spec()
	if spec.SkillsDisabledForTask(tc.SkillPaths) {
		return inputs, nil
	}
	skillPaths := r.taskSkillPaths(tc)
	inputs.SkillDirs = append([]string{}, skillPaths...)
	inputs.ExcludePaths = r.runOutputPaths()
	inputs.ExcludeEntries = r.cacheOutputEntries
	// The engine also discovers the target skill or agent in the working
	// directory, which is too broad to hash as a whole tree.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolving skill working directory: %w", err)
	}
	definition, err := execution.ResolveSkillDefinitionPath(append([]string{cwd}, skillPaths...), spec.SkillName)
	if err != nil {
		return nil, err
	}
	if definition != "" {
		content, err := os.ReadFile(definition)
		if err != nil {
			return nil, fmt.Errorf("reading skill definition %q: %w", definition, err)
		}
		// The absolute directory is injected into the system message.
		absDefinition, err := filepath.Abs(definition)
		if err != nil {
			return nil, fmt.Errorf("resolving skill definition %q: %w", definition, err)
		}
		inputs.Files = append(inputs.Files, cache.ConsumedFile{Role: "skill_definition", Path: absDefinition, Content: content})
		if dir := filepath.Dir(absDefinition); !withinAny(dir, skillPaths) {
			inputs.SkillDirs = append(inputs.SkillDirs, dir)
		}
	}
	return inputs, nil
}

// WithCacheOutputEntries excludes per-run outputs written outside the runner,
// such as timestamped result directories and session logs, from the skill
// tree fingerprint so they do not invalidate the next lookup.
func WithCacheOutputEntries(entries ...cache.ExcludeEntry) RunnerOption {
	return func(r *EvalRunner) {
		r.cacheOutputEntries = append(r.cacheOutputEntries, entries...)
	}
}

// runOutputPaths lists locations this run writes that may sit beneath a skill
// directory; they must not change the key of the next lookup.
func (r *EvalRunner) runOutputPaths() []string {
	var paths []string
	if r.cache != nil {
		paths = append(paths, r.cache.Dir())
	}
	paths = append(paths, r.cfg.TranscriptDir(), r.cfg.OutputPath())
	if r.snapshotWriter != nil {
		paths = append(paths, r.snapshotWriter.Root())
	}
	return paths
}

func withinAny(path string, roots []string) bool {
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
