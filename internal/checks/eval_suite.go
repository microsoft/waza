package checks

import (
	"fmt"
	"path/filepath"

	"github.com/microsoft/waza/internal/skill"
	"github.com/microsoft/waza/internal/workspace"
)

// EvalSuiteChecker checks for the presence of an eval.yaml file.
type EvalSuiteChecker struct{}

// EvalSuiteData holds the structured output of an eval suite check.
type EvalSuiteData struct {
	Found bool
}

var _ ComplianceChecker = (*EvalSuiteChecker)(nil)

func (c *EvalSuiteChecker) Name() string { return "eval-suite" }

func (c *EvalSuiteChecker) Check(sk skill.Skill) (*CheckResult, error) {
	evalPath, err := workspace.FindEvalForSkill(workspace.SkillInfo{
		Name:      sk.Frontmatter.Name,
		Dir:       filepath.Dir(sk.Path),
		SkillPath: sk.Path,
	})
	if err != nil {
		return nil, fmt.Errorf("finding evaluation suite: %w", err)
	}
	found := evalPath != ""

	summary := "Evaluation Suite: Not Found"
	if found {
		summary = "Evaluation Suite: Found"
	}

	return &CheckResult{
		Name:    c.Name(),
		Passed:  true, // eval is a recommendation, not a requirement
		Summary: summary,
		Data:    &EvalSuiteData{Found: found},
	}, nil
}

// Eval is a convenience wrapper that returns the typed data directly.
func (c *EvalSuiteChecker) Eval(sk skill.Skill) (*EvalSuiteData, error) {
	result, err := c.Check(sk)
	if err != nil {
		return nil, err
	}
	d, ok := result.Data.(*EvalSuiteData)
	if !ok {
		err = fmt.Errorf("unexpected data type: %T", result.Data)
	}
	return d, err
}
