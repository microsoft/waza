package graders

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

const maxAcquiredRubricBytes = 16 * 1024 * 1024

// AcquiredRubric owns source-bound bytes; selected native construction never
// resolves the original reference or an audit-copy pathname.
type AcquiredRubric struct {
	identifier, reference, declarationSHA, contentSHA string
	data                                              []byte
}

func AcquireRubric(config models.GraderConfig, declarationSHA, contentSHA string, data []byte) (*AcquiredRubric, error) {
	if len(data) == 0 || len(data) > maxAcquiredRubricBytes {
		return nil, errors.New("acquired rubric must contain 1 to 16777216 bytes")
	}
	parameters, ok := config.Parameters.(models.PromptGraderParameters)
	if !ok || strings.TrimSpace(parameters.Rubric) == "" || config.Identifier == "" || declarationSHA == "" || contentSHA == "" {
		return nil, errors.New("acquired rubric requires a complete native declaration and content binding")
	}
	digest, err := evidence.JSONDigest(config)
	if err != nil {
		return nil, fmt.Errorf("binding acquired rubric declaration: %w", err)
	}
	if digest.SHA256 != declarationSHA {
		return nil, errors.New("acquired rubric declaration binding mismatch")
	}
	owned := bytes.Clone(data)
	sum := sha256.Sum256(owned)
	if hex.EncodeToString(sum[:]) != contentSHA {
		return nil, errors.New("acquired rubric content binding mismatch")
	}
	if _, err := ParseRubric(owned); err != nil {
		return nil, fmt.Errorf("parsing acquired rubric: %w", err)
	}
	return &AcquiredRubric{config.Identifier, parameters.Rubric, declarationSHA, contentSHA, owned}, nil
}

// RunAllWithAcquiredRubrics selects immutable spec-level rubric acquisition.
// Task validators are deliberately outside this finite acquisition surface.
func RunAllWithAcquiredRubrics(ctx context.Context, configs []models.GraderConfig, tc *models.TestCase, gCtx *Context, judgeModel, judgeEffort string, updateSnapshots bool, acquired []*AcquiredRubric) (map[string]models.GraderResults, error) {
	if tc == nil || len(tc.Validators) != 0 {
		return nil, errors.New("acquired rubric execution requires a task without validators")
	}
	selected := make(map[string]*AcquiredRubric, len(acquired))
	for _, item := range acquired {
		if item == nil || selected[item.identifier] != nil {
			return nil, errors.New("missing or duplicate acquired rubric")
		}
		selected[item.identifier] = item
	}
	used := make(map[string]bool, len(selected))
	seen := make(map[string]bool, len(configs))
	for _, config := range configs {
		if config.Identifier == "" || seen[config.Identifier] {
			return nil, errors.New("missing or duplicate acquired native grader identifier")
		}
		seen[config.Identifier] = true
		parameters, prompt := config.Parameters.(models.PromptGraderParameters)
		if !prompt || strings.TrimSpace(parameters.Rubric) == "" {
			continue
		}
		item := selected[config.Identifier]
		if item == nil || used[config.Identifier] || item.reference != parameters.Rubric {
			return nil, errors.New("missing, duplicate or mismatched acquired rubric selection")
		}
		digest, err := evidence.JSONDigest(config)
		if err != nil {
			return nil, fmt.Errorf("validating acquired rubric selection: %w", err)
		}
		sum := sha256.Sum256(item.data)
		if digest.SHA256 != item.declarationSHA || hex.EncodeToString(sum[:]) != item.contentSHA {
			return nil, errors.New("acquired rubric selection binding mismatch")
		}
		used[config.Identifier] = true
	}
	if len(used) != len(selected) {
		return nil, errors.New("extra acquired rubric selection")
	}
	create := func(name string, params models.GraderParameters) (Grader, error) {
		item := selected[name]
		if item == nil {
			return Create(name, params)
		}
		parameters, ok := params.(models.PromptGraderParameters)
		if !ok {
			return nil, errors.New("acquired rubric native parameter type mismatch")
		}
		rubric, err := ParseRubric(item.data)
		if err != nil {
			return nil, fmt.Errorf("constructing acquired native rubric: %w", err)
		}
		rubric.Source = "acquired:sha256:" + item.contentSHA
		return newPromptGraderWithRubric(name, parameters, rubric)
	}
	return runAll(ctx, configs, tc, gCtx, judgeModel, judgeEffort, updateSnapshots, create)
}
