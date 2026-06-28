package specverify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyComputesDeterministicCoverage(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "SKILL.md")
	evalPath := filepath.Join(root, "eval.yaml")
	tasksDir := filepath.Join(root, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))

	writeFile(t, skillPath, `---
name: pr-summarizer
description: |
  Summarize PR diffs.
  USE FOR: summarize a PR diff.
  DO NOT USE FOR: code review security PRs.
---

# PR Summarizer

## Parameters
- repository: GitHub repository URL
`)
	writeFile(t, evalPath, `name: pr-summarizer-eval
skill: pr-summarizer
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  parallel: false
  executor: mock
  model: mock
graders:
  - type: text
    name: basic
metrics:
  - name: coverage
    weight: 1
    threshold: 1
tasks:
  - tasks/*.yaml
`)
	writeFile(t, filepath.Join(tasksDir, "use.yaml"), `id: pr-summary-basic
name: PR summary basic
description: Summarize a PR diff for a GitHub repository.
inputs:
  prompt: Please summarize this PR diff for repository https://github.com/acme/app.
expected:
  should_trigger: true
`)
	writeFile(t, filepath.Join(tasksDir, "dont.yaml"), `id: security-review-negative
name: Security review negative trigger
description: Code review security PRs should not trigger this skill.
inputs:
  prompt: Please do code review security PRs.
expected:
  should_trigger: false
`)

	report, err := Verify(context.Background(), Options{
		SkillPath:     skillPath,
		EvalPath:      evalPath,
		FailThreshold: 1,
	})
	require.NoError(t, err)

	assert.Equal(t, 4, report.Summary.TotalRequirements)
	assert.Equal(t, 4, report.Summary.CoveredRequirements)
	assert.Equal(t, 0, report.Summary.UncoveredRequirements)

	coverage := map[string]RequirementCoverage{}
	for _, row := range report.Coverage {
		coverage[row.Requirement.ID] = row
	}
	require.NotEmpty(t, coverage["req-use-001"].CoveredBy)
	assert.Equal(t, "pr-summary-basic", coverage["req-use-001"].CoveredBy[0].TaskID)
	require.NotEmpty(t, coverage["req-dont-001"].CoveredBy)
	assert.Equal(t, "security-review-negative", coverage["req-dont-001"].CoveredBy[0].TaskID)
}

func TestParseSemanticResponse(t *testing.T) {
	covered, reason := ParseSemanticResponse(`{"covered": true, "reason": "prompt matches"}`)
	assert.True(t, covered)
	assert.Equal(t, "prompt matches", reason)

	covered, reason = ParseSemanticResponse("No")
	assert.False(t, covered)
	assert.Empty(t, reason)
}

func TestTaskRefFromTestCaseSortsMetadataKeys(t *testing.T) {
	ref := taskRefFromTestCase("task.yaml", &models.TestCase{
		TestID: "metadata-order",
		Stimulus: models.TaskStimulus{
			Metadata: map[string]any{
				"zeta":  "last",
				"alpha": "first",
			},
		},
	})

	alphaIndex := strings.Index(ref.Text, "alpha\nfirst")
	zetaIndex := strings.Index(ref.Text, "zeta\nlast")
	require.NotEqual(t, -1, alphaIndex)
	require.NotEqual(t, -1, zetaIndex)
	assert.Less(t, alphaIndex, zetaIndex)
}

func TestSemanticDontRequirementOnlyConsidersNegativeTasks(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "SKILL.md")
	evalPath := filepath.Join(root, "eval.yaml")
	tasksDir := filepath.Join(root, "tasks")
	require.NoError(t, os.MkdirAll(tasksDir, 0o755))

	writeFile(t, skillPath, `---
name: pr-summarizer
description: |
  Summarize PR diffs.
  DO NOT USE FOR: code review security PRs.
---
`)
	writeFile(t, evalPath, `name: pr-summarizer-eval
skill: pr-summarizer
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  parallel: false
  executor: mock
  model: mock
graders:
  - type: text
    name: basic
metrics:
  - name: coverage
    weight: 1
    threshold: 1
tasks:
  - tasks/*.yaml
`)
	writeFile(t, filepath.Join(tasksDir, "positive.yaml"), `id: positive-task
name: Positive task
description: Code review security PRs.
inputs:
  prompt: Please do code review security PRs.
expected:
  should_trigger: true
`)

	report, err := Verify(context.Background(), Options{
		SkillPath:     skillPath,
		EvalPath:      evalPath,
		Semantic:      true,
		FailThreshold: 1,
		Matcher:       alwaysMatchSemanticMatcher{},
	})
	require.NoError(t, err)

	var dont RequirementCoverage
	for _, row := range report.Coverage {
		if row.Requirement.ID == "req-dont-001" {
			dont = row
			break
		}
	}
	require.Equal(t, "req-dont-001", dont.Requirement.ID)
	assert.Empty(t, dont.CoveredBy)
}

func TestVerifyLoadsCSVTasksFromDataset(t *testing.T) {
	root := t.TempDir()
	skillPath := filepath.Join(root, "SKILL.md")
	evalPath := filepath.Join(root, "eval.yaml")
	csvPath := filepath.Join(root, "tasks.csv")

	writeFile(t, skillPath, `---
name: doc-writer
description: |
  Write docs.
  USE FOR: write onboarding docs.
---
`)
	writeFile(t, evalPath, `name: doc-writer-eval
skill: doc-writer
version: "1.0"
config:
  trials_per_task: 1
  timeout_seconds: 60
  parallel: false
  executor: mock
  model: mock
graders:
  - type: text
    name: basic
metrics:
  - name: coverage
    weight: 1
    threshold: 1
tasks_from: tasks.csv
`)
	writeFile(t, csvPath, "id,name,prompt\nonboarding-docs,Onboarding docs,Please write onboarding docs for the team\n")

	report, err := Verify(context.Background(), Options{
		SkillPath:     skillPath,
		EvalPath:      evalPath,
		FailThreshold: 1,
	})
	require.NoError(t, err)

	coverage := map[string]RequirementCoverage{}
	for _, row := range report.Coverage {
		coverage[row.Requirement.ID] = row
	}
	require.NotEmpty(t, coverage["req-use-001"].CoveredBy)
	assert.Equal(t, "onboarding-docs", coverage["req-use-001"].CoveredBy[0].TaskID)
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

type alwaysMatchSemanticMatcher struct{}

func (alwaysMatchSemanticMatcher) Matches(context.Context, Requirement, TaskRef) (bool, string, error) {
	return true, "matched", nil
}
