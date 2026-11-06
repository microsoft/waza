package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestBuildCoverageReport_NoEvals(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeSkill(t, root, filepath.Join("skills", "beta"), "beta")

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)

	assert.Equal(t, 2, report.TotalSkills)
	assert.Equal(t, 0, report.Covered)
	assert.Equal(t, 0, report.Partial)
	assert.Equal(t, 2, report.Uncovered)
	assert.Equal(t, "❌ None", report.Skills[0].Coverage)
	assert.Equal(t, "❌ None", report.Skills[1].Coverage)
}

func TestBuildCoverageReport_PartialAndFull(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "partial-skill"), "partial-skill")
	writeSkill(t, root, filepath.Join(".github", "skills", "full-skill"), "full-skill")

	writeEval(t, root, filepath.Join("evals", "partial-skill", "eval.yaml"), `
skill: partial-skill
tasks:
  - tasks/*.yaml
graders:
  - type: prompt
    name: judge
`)
	writeEval(t, root, filepath.Join("custom", "full-skill", "eval.yaml"), `
skill: full-skill
tasks:
  - tasks/a.yaml
  - tasks/b.yaml
graders:
  - type: prompt
    name: judge
  - type: file
    name: files
    config:
      must_exist:
        - output.txt
`)
	writeTask(t, root, filepath.Join("evals", "partial-skill", "tasks", "a.yaml"), "partial-a")
	writeTask(t, root, filepath.Join("custom", "full-skill", "tasks", "a.yaml"), "full-a")
	writeTask(t, root, filepath.Join("custom", "full-skill", "tasks", "b.yaml"), "full-b")

	report, err := buildCoverageReport(root, []string{"custom"})
	require.NoError(t, err)

	assert.Equal(t, 2, report.TotalSkills)
	assert.Equal(t, 1, report.Covered)
	assert.Equal(t, 1, report.Partial)
	assert.Equal(t, 0, report.Uncovered)
	assert.InDelta(t, 50.0, report.CoveragePct, 0.1)

	rows := map[string]coverageSkillRow{}
	for _, row := range report.Skills {
		rows[row.Skill] = row
	}

	assert.Equal(t, "⚠️ Partial", rows["partial-skill"].Coverage)
	assert.Equal(t, 1, rows["partial-skill"].Tasks)
	assert.Equal(t, []string{"prompt"}, rows["partial-skill"].Graders)

	assert.Equal(t, "✅ Full", rows["full-skill"].Coverage)
	assert.Equal(t, 2, rows["full-skill"].Tasks)
	assert.Equal(t, []string{"file", "prompt"}, rows["full-skill"].Graders)
}

func TestBuildCoverageReport_IncludesEvalYML(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeEval(t, root, filepath.Join("evals", "alpha", "eval.yml"), `
skill: alpha
tasks:
  - tasks/*.yaml
graders:
  - type: prompt
    name: judge
  - type: file
    name: files
    config:
      must_exist:
        - output.txt
`)
	writeTask(t, root, filepath.Join("evals", "alpha", "tasks", "a.yaml"), "alpha-a")

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, "✅ Full", report.Skills[0].Coverage)
}

func TestBuildCoverageReport_WarnsOnParseErrors(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeEval(t, root, filepath.Join("evals", "alpha", "eval.yaml"), "skill: [bad")

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err, "parse failures should warn, not error")
	require.NotNil(t, report)
	assert.Equal(t, 1, report.TotalSkills)
	assert.Equal(t, 0, report.Covered)
}

func TestRenderCoverageMarkdown(t *testing.T) {
	report := &coverageReport{
		TotalSkills: 2,
		Skills: []coverageSkillRow{
			{Skill: "alpha", Tasks: 1, Graders: []string{"prompt"}, Coverage: "⚠️ Partial"},
			{Skill: "beta", Tasks: 2, Graders: []string{"file", "prompt"}, Coverage: "✅ Full"},
		},
	}

	var buf bytes.Buffer
	renderCoverageMarkdown(&buf, report)
	out := buf.String()

	assert.Contains(t, out, "📊 Eval Coverage Grid")
	assert.Contains(t, out, "| Skill | Tasks | Graders | Coverage |")
	assert.Contains(t, out, "| alpha | 1 | prompt | ⚠️ Partial |")
	assert.Contains(t, out, "| beta | 2 | file, prompt | ✅ Full |")
}

func TestRenderCoverageJSON(t *testing.T) {
	report := &coverageReport{
		TotalSkills: 1,
		Covered:     1,
		Partial:     0,
		Uncovered:   0,
		CoveragePct: 100,
		Skills: []coverageSkillRow{
			{Skill: "alpha", Tasks: 2, Graders: []string{"file", "prompt"}, Coverage: "✅ Full"},
		},
	}

	var buf bytes.Buffer
	require.NoError(t, renderCoverageJSON(&buf, report))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	assert.Equal(t, float64(1), decoded["total_skills"])
	assert.Contains(t, buf.String(), "\n  \"total_skills\"")
}

func TestCoverageCommand_UnsupportedFormat(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")

	cmd := newCoverageCommand()
	cmd.SetOut(new(bytes.Buffer))
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{root, "--format", "xml"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported format "xml"`)
}

func TestRootCommand_HasCoverageSubcommand(t *testing.T) {
	root := newRootCommand()
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "coverage" {
			found = true
			break
		}
	}
	assert.True(t, found, "root command should have 'coverage' subcommand")
}

func TestBuildCoverageReport_RejectsFilePath(t *testing.T) {
	f := filepath.Join(t.TempDir(), "notadir.txt")
	require.NoError(t, os.WriteFile(f, []byte("hello"), 0o644))

	_, err := buildCoverageReport(f, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a directory")
}

func TestBuildCoverageReport_TasksFromCountsAsTasks(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "from-skill"), "from-skill")
	writeEval(t, root, filepath.Join("evals", "from-skill", "eval.yaml"), `
skill: from-skill
tasks_from: tasks.csv
graders:
  - type: prompt
    name: judge
  - type: diff
    name: snapshot
    config:
      expected_files:
        - path: output.txt
`)
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "evals", "from-skill", "tasks.csv"),
		[]byte("id,name,prompt\nfirst,First,Say hello.\n"),
		0o644,
	))

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, "✅ Full", report.Skills[0].Coverage)
	assert.Equal(t, 1, report.Skills[0].Tasks)
}

func writeSkill(t *testing.T, root, relDir, skillName string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := `---
name: ` + skillName + `
description: "test skill"
---
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

func writeEval(t *testing.T, root, relPath, content string) {
	t.Helper()
	absPath := filepath.Join(root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))
}

func writeTask(t *testing.T, root, relPath, id string) {
	t.Helper()
	absPath := filepath.Join(root, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	content := "id: " + id + "\nname: " + id + "\ninputs:\n  prompt: Say hello.\n"
	require.NoError(t, os.WriteFile(absPath, []byte(content), 0o644))
}

func writeAgentFile(t *testing.T, root, relDir, agentName string) {
	t.Helper()
	dir := filepath.Join(root, relDir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := `---
name: ` + agentName + `
description: "test agent"
tools:
  - search/codebase
model: claude-sonnet-4
---

You are a test agent.
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, agentName+".agent.md"), []byte(content), 0o644))
}

func TestBuildCoverageReport_DiscoverAgentFiles(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "alpha"), "alpha")
	writeAgentFile(t, root, filepath.Join("skills", "my-agent"), "my-agent")

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)

	assert.Equal(t, 2, report.TotalSkills)

	names := make(map[string]bool)
	for _, row := range report.Skills {
		names[row.Skill] = true
	}
	assert.True(t, names["alpha"], "should find SKILL.md skill")
	assert.True(t, names["my-agent"], "should find .agent.md agent")
}

func TestBuildCoverageReport_SkillMDPriorityOverAgentMD(t *testing.T) {
	root := t.TempDir()

	// Write both SKILL.md and .agent.md in the same directory
	dir := filepath.Join(root, "skills", "both")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	skillContent := `---
name: skill-version
description: "from SKILL.md"
---
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillContent), 0o644))

	agentContent := `---
name: agent-version
description: "from agent.md"
---
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.agent.md"), []byte(agentContent), 0o644))

	report, err := buildCoverageReport(root, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, report.TotalSkills)
	assert.Equal(t, "skill-version", report.Skills[0].Skill)
}

func TestCoverageCommand_UsesExecutionTaskSelection(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeTask(t, root, filepath.Join("evals", "greeting", "tasks", "first.yaml"), "first")
	writeTask(t, root, filepath.Join("evals", "greeting", "tasks", "second.yaml"), "second")
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "evals", "greeting", "tasks.csv"),
		[]byte("id,name,prompt\nfirst,First,Say hello.\nsecond,Second,Say hi.\n"),
		0o644,
	))

	evalPath := filepath.Join("evals", "greeting", "eval.yaml")
	cases := []struct {
		name         string
		selection    string
		wantTasks    int
		wantCoverage string
	}{
		{name: "glob", selection: "tasks:\n  - tasks/*.yaml", wantTasks: 2, wantCoverage: "✅ Full"},
		{name: "explicit", selection: "tasks:\n  - tasks/first.yaml\n  - tasks/second.yaml", wantTasks: 2, wantCoverage: "✅ Full"},
		{name: "empty glob", selection: "tasks:\n  - tasks/missing-*.yaml", wantTasks: 0, wantCoverage: "⚠️ Partial"},
		{name: "selected CSV range", selection: "tasks_from: tasks.csv\nrange: [2, 2]", wantTasks: 1, wantCoverage: "✅ Full"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeEval(t, root, evalPath, `
schemaVersion: "1.4"
name: coverage-selection
skill: greeting
config:
  executor: mock
  model: mock
  trials_per_task: 1
  timeout_seconds: 5
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
`+tc.selection+`
graders:
  - name: output
    type: text
    config: {regex_match: [".+"]}
  - name: js
    type: code
    config: {language: javascript, assertions: ["output.length > 0"]}
`)

			spec, err := parseEvalSpec(filepath.Join(root, evalPath))
			require.NoError(t, err)
			selected, selectionErr := orchestration.LoadSelectedTestCasesOffline(
				spec.selectionSpec(),
				filepath.Dir(filepath.Join(root, evalPath)),
				time.Unix(0, 0).UTC(),
			)
			if tc.wantTasks == 0 {
				require.Error(t, selectionErr)
			} else {
				require.NoError(t, selectionErr)
				require.Len(t, selected, tc.wantTasks)
			}

			var stdout bytes.Buffer
			cmd := newCoverageCommand()
			cmd.SetOut(&stdout)
			cmd.SetErr(new(bytes.Buffer))
			cmd.SetArgs([]string{root, "--format", "json"})
			require.NoError(t, cmd.Execute())

			var report coverageReport
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
			require.Len(t, report.Skills, 1)
			assert.Equal(t, tc.wantTasks, report.Skills[0].Tasks)
			assert.Equal(t, tc.wantCoverage, report.Skills[0].Coverage)
			if tc.wantTasks == 0 {
				assert.Zero(t, report.CoveragePct)
			}
		})
	}
}

func TestCoverageCommand_FailedSelectionDoesNotContributeGraders(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeTask(t, root, filepath.Join("evals", "greeting", "selected", "tasks", "first.yaml"), "first")
	writeEval(t, root, filepath.Join("evals", "greeting", "selected", "eval.yaml"), `
schemaVersion: "1.4"
name: selected-tasks
skill: greeting
config:
  executor: mock
tasks: [tasks/*.yaml]
graders:
  - name: output
    type: text
    config: {regex_match: [".+"]}
`)
	writeEval(t, root, filepath.Join("evals", "greeting", "missing", "eval.yaml"), `
schemaVersion: "1.4"
name: missing-tasks
skill: greeting
config:
  executor: mock
tasks: [tasks/missing-*.yaml]
graders:
  - name: js
    type: code
    config: {language: javascript, assertions: ["output.length > 0"]}
`)

	report := executeCoverageJSON(t, root)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, 1, report.Skills[0].Tasks)
	assert.Equal(t, []string{"text"}, report.Skills[0].Graders)
	assert.Equal(t, "⚠️ Partial", report.Skills[0].Coverage)
	assert.Zero(t, report.CoveragePct)
}

func TestCoverageCommand_WholeGraderAliasPreservesTasks(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeTask(t, root, filepath.Join("evals", "greeting", "tasks", "first.yaml"), "first")
	evalPath := filepath.Join(root, "evals", "greeting", "eval.yaml")
	writeEval(t, root, filepath.Join("evals", "greeting", "eval.yaml"), `
schemaVersion: "1.4"
name: aliased-grader
skill: greeting
config:
  executor: mock
  model: mock
  trials_per_task: 1
  timeout_seconds: 5
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
tasks: [tasks/*.yaml]
graders:
  - &output
    name: output
    type: text
    config: {regex_match: [".+"]}
  - *output
`)

	_, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)

	report := executeCoverageJSON(t, root)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, 1, report.Skills[0].Tasks)
	assert.Equal(t, []string{"text"}, report.Skills[0].Graders)
	assert.Equal(t, "⚠️ Partial", report.Skills[0].Coverage)
}

func TestStandaloneYAMLNode_RejectsNestedRepeatedAliases(t *testing.T) {
	budget := maxGraderYAMLExpansionNodes

	_, err := standaloneYAMLNode(repeatedAliasNode(15), make(map[*yaml.Node]bool), 0, &budget)

	require.EqualError(t, err, "grader YAML alias expansion exceeds 100000 nodes")
}

func TestStandaloneYAMLNode_BoundsAggregateExpansion(t *testing.T) {
	budget := maxGraderYAMLExpansionNodes

	_, err := standaloneYAMLNode(repeatedAliasNode(14), make(map[*yaml.Node]bool), 0, &budget)
	require.NoError(t, err)
	_, err = standaloneYAMLNode(repeatedAliasNode(14), make(map[*yaml.Node]bool), 0, &budget)

	require.EqualError(t, err, "grader YAML alias expansion exceeds 100000 nodes")
}

func repeatedAliasNode(levels int) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "value"}
	for range levels {
		target := node
		node = &yaml.Node{
			Kind: yaml.SequenceNode,
			Content: []*yaml.Node{
				{Kind: yaml.AliasNode, Alias: target},
				{Kind: yaml.AliasNode, Alias: target},
			},
		}
	}
	return node
}

func TestCoverageCommand_InvalidGraderCannotReportFull(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeTask(t, root, filepath.Join("evals", "greeting", "tasks", "first.yaml"), "first")
	writeEval(t, root, filepath.Join("evals", "greeting", "eval.yaml"), `
schemaVersion: "1.4"
name: invalid-grader
skill: greeting
config:
  executor: mock
  model: mock
  trials_per_task: 1
  timeout_seconds: 5
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
tasks: [tasks/*.yaml]
graders:
  - name: output
    type: text
    config: {regex_match: [".+"]}
  - name: js
    type: code
    config: {}
`)

	report := executeCoverageJSON(t, root)
	require.Len(t, report.Skills, 1)
	assert.NotEqual(t, "✅ Full", report.Skills[0].Coverage)
	assert.Zero(t, report.CoveragePct)
}

func TestCoverageCommand_TaskSelectionDoesNotLoadExternalSchemas(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"type":"object"}`))
	}))
	defer server.Close()

	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeEval(t, root, filepath.Join("evals", "greeting", "eval.yaml"), `
schemaVersion: "1.4"
name: external-task-schema
skill: greeting
config:
  executor: copilot-sdk
  model: mock
  trials_per_task: 1
  timeout_seconds: 5
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
tasks: [tasks/*.yaml]
graders:
  - name: output
    type: text
    config: {regex_match: [".+"]}
  - name: files
    type: file
    config: {must_exist: [output.txt]}
`)
	writeEval(t, root, filepath.Join("evals", "greeting", "tasks", "first.yaml"), `
id: first
name: first
inputs:
  prompt: Say hello.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        responses:
          - match_schema:
              $ref: `+server.URL+`/schema.json
            return: {answer: hello}
`)

	report := executeCoverageJSON(t, root)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, 0, report.Skills[0].Tasks)
	assert.Equal(t, "⚠️ Partial", report.Skills[0].Coverage)
	assert.Zero(t, requests.Load())
}

func TestCoverageCommand_InvalidMCPInputSchemaCannotReportFull(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, filepath.Join("skills", "greeting"), "greeting")
	writeEval(t, root, filepath.Join("evals", "greeting", "eval.yaml"), `
schemaVersion: "1.4"
name: invalid-mcp-input-schema
skill: greeting
config:
  executor: copilot-sdk
  model: mock
  trials_per_task: 1
  timeout_seconds: 5
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
tasks: [tasks/*.yaml]
graders:
  - name: output
    type: text
    config: {regex_match: [".+"]}
  - name: files
    type: file
    config: {must_exist: [output.txt]}
`)
	writeEval(t, root, filepath.Join("evals", "greeting", "tasks", "first.yaml"), `
id: first
name: first
inputs:
  prompt: Say hello.
mcp_mocks:
  - name: host
    tools:
      ask_user:
        input_schema: {type: 42}
        responses:
          - return: {answer: hello}
`)

	report := executeCoverageJSON(t, root)
	require.Len(t, report.Skills, 1)
	assert.Equal(t, 0, report.Skills[0].Tasks)
	assert.Equal(t, "⚠️ Partial", report.Skills[0].Coverage)
}

func executeCoverageJSON(t *testing.T, root string) coverageReport {
	t.Helper()
	var stdout bytes.Buffer
	cmd := newCoverageCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(new(bytes.Buffer))
	cmd.SetArgs([]string{root, "--format", "json"})
	require.NoError(t, cmd.Execute())

	var report coverageReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	return report
}
