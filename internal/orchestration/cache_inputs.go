package orchestration

import (
	"fmt"
	"os"
	"path/filepath"

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
	inputs.SkillDirs = skillPaths
	// The engine also discovers the target skill or agent in the working
	// directory, which is too broad to hash as a tree.
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
		inputs.Files = append(inputs.Files, cache.ConsumedFile{Role: "skill_definition", Path: filepath.Base(definition), Content: content})
	}
	return inputs, nil
}
