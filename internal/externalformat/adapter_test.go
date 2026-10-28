package externalformat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var selected = Selection{Format: Format, Version: Version, Profile: Profile}

func conformanceSource(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile("testdata/contains.eval.yaml")
	require.NoError(t, err)
	return source
}

func loadNative(t *testing.T, file TaskFile) *models.TestCase {
	t.Helper()
	task, err := models.ParseCapturedTestCaseOffline([]byte(file.Content), file.Path, func(path string) ([]byte, error) {
		t.Errorf("unexpected source read: %s", path)
		return nil, fmt.Errorf("unexpected source read: %s", path)
	})
	require.NoError(t, err)
	return task
}

// This oracle is recorded by the separately executed, pinned upstream code,
// not computed by either the adapter or Waza's grader implementation.
func TestConvertedNativeDecisionsMatchUpstream(t *testing.T) {
	oracleBytes, err := os.ReadFile("testdata/reference/observed.json")
	require.NoError(t, err)
	var oracle struct {
		Upstream     string `json:"upstream"`
		Observations []struct {
			ID      string  `json:"id"`
			Output  string  `json:"output"`
			Score   float64 `json:"score"`
			Verdict string  `json:"verdict"`
		} `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(oracleBytes, &oracle))
	require.Equal(t, "EntityProcess/agentv@68df6d2206cea61f721d0445b405efcb5e94aff0", oracle.Upstream)
	require.Len(t, oracle.Observations, 12)

	suite, err := Import(conformanceSource(t), selected)
	require.NoError(t, err)
	require.Equal(t, "contains-conformance", suite.Name)
	require.Len(t, suite.Tasks, 3)
	tasks := make(map[string]*models.TestCase)
	for i, file := range suite.Tasks {
		task := loadNative(t, file)
		require.Equal(t, []string{"world", "unicode", "exact"}[i], task.TestID)
		require.Equal(t, fmt.Sprintf("tasks/%04d.yaml", i+1), file.Path)
		require.NotContains(t, tasks, task.TestID)
		tasks[task.TestID] = task
	}
	for i, observation := range oracle.Observations {
		t.Run(fmt.Sprintf("%s-%d", observation.ID, i), func(t *testing.T) {
			task, ok := tasks[observation.ID]
			require.True(t, ok, "oracle must identify a real converted task")
			require.Len(t, task.Validators, 1)
			results, err := graders.RunAll(context.Background(), nil, task,
				&graders.Context{Output: observation.Output, OutputPresent: true}, "", "", false)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Contains(t, results, "contains")
			result := results["contains"]
			require.Equal(t, observation.Score, result.Score)
			require.Equal(t, observation.Verdict == "pass", result.Passed)
			require.Equal(t, float64(1), result.Weight)
		})
	}
}

func TestSemanticRoundTripAndNativeEdits(t *testing.T) {
	suite, err := Import(conformanceSource(t), selected)
	require.NoError(t, err)
	external, err := Export(*suite, selected)
	require.NoError(t, err)
	again, err := Import(external, selected)
	require.NoError(t, err)
	require.Equal(t, suite, again)
	// Export reads the actual native content, not a saved copy of the input.
	before := suite.Tasks[0].Content
	suite.Tasks[0].Content = strings.ReplaceAll(before, "world", "planet")
	require.NotEqual(t, before, suite.Tasks[0].Content)
	external, err = Export(*suite, selected)
	require.NoError(t, err)
	require.Contains(t, string(external), "id: planet")
	require.Contains(t, string(external), "value: planet")
	edited, err := Import(external, selected)
	require.NoError(t, err)
	task := loadNative(t, edited.Tasks[0])
	require.Equal(t, "planet", task.TestID)
	require.Equal(t, "Say hello to the planet.", task.Stimulus.Message)
	for _, control := range []struct {
		output string
		passed bool
	}{{"Hello planet", true}, {"Hello world", false}} {
		results, err := graders.RunAll(context.Background(), nil, task,
			&graders.Context{Output: control.output, OutputPresent: true}, "", "", false)
		require.NoError(t, err)
		require.Equal(t, control.passed, results["contains"].Passed)
	}
}

func TestAlternativeValidDefinitions(t *testing.T) {
	source := string(conformanceSource(t))
	for name, input := range map[string]string{
		"CRLF":              strings.ReplaceAll(source, "\n", "\r\n"),
		"quoted identity":   strings.Replace(source, "id: world", `id: "world"`, 1),
		"literal multiline": strings.Replace(source, "input: Say hello to the world.", "input: |\n        Say hello to the world.\n        Preserve this second line.", 1),
		"field order":       strings.Replace(source, "name: contains-conformance\nrequires:\n  agentv: \"5.3.3\"", "requires:\n  agentv: \"5.3.3\"\nname: contains-conformance", 1),
		"literal zero":      strings.Replace(source, "value: world", `value: "0"`, 1),
		"literal false":     strings.Replace(source, "value: world", `value: "false"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEqual(t, source, input)
			suite, err := Import([]byte(input), selected)
			require.NoError(t, err)
			external, err := Export(*suite, selected)
			require.NoError(t, err)
			again, err := Import(external, selected)
			require.NoError(t, err)
			require.Equal(t, suite, again)
			if name == "literal zero" || name == "literal false" {
				expected := "0"
				if name == "literal false" {
					expected = "false"
				}
				task := loadNative(t, suite.Tasks[0])
				for _, output := range []string{expected, ""} {
					results, err := graders.RunAll(context.Background(), nil, task,
						&graders.Context{Output: output, OutputPresent: true}, "", "", false)
					require.NoError(t, err)
					require.Equal(t, output == expected, results["contains"].Passed)
				}
			}
		})
	}
}

func TestWholeSuiteRejection(t *testing.T) {
	source := string(conformanceSource(t))
	replacements := []struct {
		name, old, replacement, diagnostic string
	}{
		{"wrong pin", `"5.3.3"`, `"5.3.2"`, "exact pinned version"},
		{"version range", `"5.3.3"`, `"^5.3.3"`, "exact pinned version"},
		{"provider", "name: contains-conformance", "name: contains-conformance\nproviders: [openai]", "$.providers"},
		{"judge", "type: contains", "type: llm-judge", "only contains"},
		{"criteria", "value: world", "value: world\n        criteria: be helpful", ".criteria"},
		{"transform", "value: world", "value: world\n        transform: output.trim()", ".transform"},
		{"extension", "name: contains-conformance", "name: contains-conformance\nextensions: [run.ts]", "$.extensions"},
		{"include", "name: contains-conformance", "name: contains-conformance\ninclude: ../suite.yaml", "$.include"},
		{"prompt reference", `"{{ input }}"`, `"file://prompt.md"`, "identity template"},
		{"custom prompt", `"{{ input }}"`, `"Hello {{ input }}"`, "identity template"},
		{"input template", "Say hello to the world.", "'{{ env.SECRET }}'", "literal text"},
		{"input template comment", "Say hello to the world.", "'hello {# removed #}'", "literal text"},
		{"assert template", "value: world", "value: '{{ input }}'", "literal substring"},
		{"input file", "Say hello to the world.", "file://secret.txt", "literal text"},
		{"null", "value: world", "value: null", "literal strings only"},
		{"empty substring", "value: world", `value: ""`, "nonempty literal"},
		{"blank input", "Say hello to the world.", "' '", "literal text"},
		{"missing input", "vars:\n      input: Say hello to the world.", "vars: {}", ".input: required"},
		{"boolean", "value: world", "value: false", "literal strings only"},
		{"zero", "value: world", "value: 0", "literal strings only"},
		{"precision", "value: world", "value: 9007199254740993", "literal strings only"},
		{"weight zero", "value: world", "value: world\n        weight: 0", "literal strings only"},
		{"weight default", "value: world", "value: world\n        weight: 1", "literal strings only"},
		{"threshold", "value: world", "value: world\n        min_score: 0.8", "literal strings only"},
		{"required false", "value: world", "value: world\n        required: false", "literal strings only"},
		{"required true", "value: world", "value: world\n        required: true", "literal strings only"},
		{"negate", "value: world", "value: world\n        negate: false", "literal strings only"},
		{"metric", "value: world", "value: world\n        metric: presence", ".metric: unsupported"},
		{"extra assertion", "value: world", "value: world\n      - type: contains\n        value: other", "1-1 items"},
		{"extra var", "input: Say hello to the world.", "input: Say hello to the world.\n      other: ignored", ".other"},
		{"duplicate identity", "id: unicode", "id: world", "duplicate task identity"},
		{"unsafe identity", "id: world", "id: ../world", "paths are not task identities"},
		{"duplicate key", "value: world", "value: world\n        value: other", "unique literal string keys"},
		{"alias", "value: world", "value: &x world", "anchors"},
		{"custom tag", "value: world", "value: !execute world", "literal strings only"},
		{"merge", "value: world", "value: world\n        <<: {weight: '0'}", "unique literal string keys"},
		{"root schema version", "name: contains-conformance", "name: contains-conformance\nschema_version: '1.0'", "$.schema_version"},
		{"execution threshold", "name: contains-conformance", "name: contains-conformance\nexecution: {threshold: '0.8'}", "$.execution"},
		{"tool requirement", "agentv: \"5.3.3\"", "agentv: \"5.3.3\"\n  tools: [shell]", "$.requires.tools"},
		{"requires shape", "requires:\n  agentv: \"5.3.3\"", "requires: '5.3.3'", "$.requires: expected a mapping"},
		{"prompt shape", "prompts:\n  - \"{{ input }}\"", "prompts: '{{ input }}'", "$.prompts: expected a sequence"},
		{"vars shape", "vars:\n      input: Say hello to the world.", "vars: text", ".vars: expected a mapping"},
	}
	for _, test := range replacements {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(source, test.old, test.replacement, 1)
			require.NotEqual(t, source, input)
			suite, err := Import([]byte(input), selected)
			require.ErrorContains(t, err, test.diagnostic)
			require.Nil(t, suite, "no partial suite may escape")
		})
	}
	for name, input := range map[string][]byte{
		"multiple documents": []byte(source + "\n---\n{}\n"),
		"malformed":          []byte("name: ["),
		"invalid UTF8":       {0xff, 0xfe},
		"empty":              {},
		"oversize":           []byte(strings.Repeat(" ", MaxBytes+1)),
		"scalar":             []byte("hello"),
		"tagged map":         []byte("!map {}"),
		"tagged sequence":    []byte("!seq []"),
		"deep nesting":       []byte("name: " + strings.Repeat("[", 40) + "x" + strings.Repeat("]", 40)),
		"node limit":         []byte("[" + strings.Repeat("x,", maxNodes) + "x]"),
		"empty tasks":        []byte(source[:strings.Index(source, "tests:")] + "tests: []\n"),
	} {
		t.Run(name, func(t *testing.T) {
			suite, err := Import(input, selected)
			require.Error(t, err)
			require.Nil(t, suite)
		})
	}
}

func TestExportRejectsNativeFeaturesByPresence(t *testing.T) {
	for name, mutate := range map[string]func(*Suite){
		"unsafe path":    func(s *Suite) { s.Tasks[0].Path = "../task.yaml" },
		"reordered path": func(s *Suite) { s.Tasks[0], s.Tasks[1] = s.Tasks[1], s.Tasks[0] },
		"display name": func(s *Suite) {
			s.Tasks[0].Content = strings.Replace(s.Tasks[0].Content, "name: world", "name: separate", 1)
		},
		"skill paths empty": func(s *Suite) { s.Tasks[0].Content += "skill_directories: []\n" },
		"context directory": func(s *Suite) { s.Tasks[0].Content += "context_dir: ../fixtures\n" },
		"disabled":          func(s *Suite) { s.Tasks[0].Content += "enabled: false\n" },
		"summary empty":     func(s *Suite) { s.Tasks[0].Content += "description: ''\n" },
		"weight": func(s *Suite) {
			s.Tasks[0].Content = strings.Replace(s.Tasks[0].Content, "type: text", "weight: 1\n      type: text", 1)
		},
		"prompt file": func(s *Suite) {
			s.Tasks[0].Content = strings.Replace(s.Tasks[0].Content, "prompt:", "prompt_file:", 1)
		},
		"duplicate tasks": func(s *Suite) { s.Tasks[1].Content = s.Tasks[0].Content },
		"no tasks":        func(s *Suite) { s.Tasks = nil },
		"invalid name":    func(s *Suite) { s.Name = "../suite" },
	} {
		t.Run(name, func(t *testing.T) {
			suite, err := Import(conformanceSource(t), selected)
			require.NoError(t, err)
			mutate(suite)
			for _, file := range suite.Tasks {
				var document yaml.Node
				require.NoError(t, yaml.Unmarshal([]byte(file.Content), &document), "negative control must remain valid YAML")
			}
			data, err := Export(*suite, selected)
			require.Error(t, err)
			require.Nil(t, data)
		})
	}
}

func TestExportRejectsValidNativeExtensions(t *testing.T) {
	for _, extension := range []string{"another rule", "another grader", "resource", "follow-up"} {
		t.Run(extension, func(t *testing.T) {
			suite, err := Import(conformanceSource(t), selected)
			require.NoError(t, err)
			task := loadNative(t, suite.Tasks[0])
			switch extension {
			case "another rule":
				params, ok := task.Validators[0].Parameters.(models.TextGraderParameters)
				require.True(t, ok)
				params.Contains = []string{"other"}
				task.Validators[0].Parameters = params
			case "another grader":
				second := task.Validators[0]
				second.Identifier = "second"
				task.Validators = append(task.Validators, second)
			case "resource":
				task.Stimulus.Resources = []models.ResourceRef{{Location: "fixture.txt", Body: "fixture"}}
			case "follow-up":
				task.Stimulus.FollowUps = []string{"Then do something else."}
			}
			modified, err := yaml.Marshal(task)
			require.NoError(t, err)
			require.NotEqual(t, suite.Tasks[0].Content, string(modified))
			suite.Tasks[0].Content = string(modified)
			loadNative(t, suite.Tasks[0]) // prove the rejected extension is valid native input
			output, err := Export(*suite, selected)
			require.Error(t, err)
			require.Nil(t, output)
		})
	}
}

func TestTaskAndSizeLimits(t *testing.T) {
	single := externalSuite{Name: "sized", Requires: externalRequires{AgentV: Version},
		Prompts: []string{"{{ input }}"}}
	for i := range MaxTasks {
		single.Tests = append(single.Tests, externalTest{
			ID: fmt.Sprintf("t%d", i), Vars: externalVars{Input: "hello"},
			Assert: []externalAssertion{{Type: "contains", Value: "hello"}},
		})
	}
	data, err := yaml.Marshal(single)
	require.NoError(t, err)
	require.Less(t, len(data), MaxBytes)
	suite, err := Import(data, selected)
	require.NoError(t, err)
	require.Len(t, suite.Tasks, MaxTasks)
	_, err = Export(*suite, selected)
	require.NoError(t, err)

	single.Tests = append(single.Tests, externalTest{ID: "last", Vars: externalVars{Input: "hello"},
		Assert: []externalAssertion{{Type: "contains", Value: "hello"}}})
	data, err = yaml.Marshal(single)
	require.NoError(t, err)
	suite, err = Import(data, selected)
	require.ErrorContains(t, err, "1-1000 items")
	require.Nil(t, suite)

	for _, control := range []struct {
		lines      int
		diagnostic string
	}{
		{MaxBytes / 4, "converted native tasks exceed"},
		{MaxBytes / 12, "canonical external form exceeds"},
	} {
		// Compact flow-style source expands into indented multiline strings.
		input := "name: sized\nrequires: {agentv: '5.3.3'}\nprompts: ['{{ input }}']\n" +
			"tests: [{id: t0, vars: {input: " + strconv.Quote(strings.Repeat("x\n", control.lines)) +
			"}, assert: [{type: contains, value: hello}]}]\n"
		require.Less(t, len(input), MaxBytes)
		suite, err = Import([]byte(input), selected)
		require.ErrorContains(t, err, control.diagnostic)
		require.Nil(t, suite)
	}

	data = conformanceSource(t)
	data = append(data, '#')
	data = append(data, []byte(strings.Repeat(" ", MaxBytes-len(data)))...)
	require.Len(t, data, MaxBytes)
	_, err = Import(data, selected)
	require.NoError(t, err)
	_, err = Import(append(data, ' '), selected)
	require.ErrorContains(t, err, "valid UTF-8")
}

func TestSelectionIsExplicit(t *testing.T) {
	for _, selection := range []Selection{
		{}, {Format: "agentevals", Version: "1.0", Profile: Profile},
		{Format: Format, Version: "latest", Profile: Profile},
		{Format: Format, Version: Version},
	} {
		suite, err := Import(conformanceSource(t), selection)
		require.ErrorContains(t, err, "select agentv/5.3.3/contains-v1 explicitly")
		require.Nil(t, suite)
		data, err := Export(Suite{}, selection)
		require.Error(t, err)
		require.Nil(t, data)
	}
}

func TestIndependentTaskDiagnostics(t *testing.T) {
	input := strings.Replace(string(conformanceSource(t)), "id: world", "id: ../world", 1)
	input = strings.Replace(input, "id: unicode", "id: ../unicode", 1)
	suite, err := Import([]byte(input), selected)
	require.ErrorContains(t, err, "$.tests[0].id")
	require.ErrorContains(t, err, "$.tests[1].id")
	require.Nil(t, suite)
}

func FuzzAdmission(f *testing.F) {
	for _, seed := range []string{"", "{}", "[]", "name: [", "value: &a [*a]", "!!null"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, source []byte) {
		suite, err := Import(source, selected)
		if err != nil {
			require.Nil(t, suite)
			return
		}
		exported, err := Export(*suite, selected)
		require.NoError(t, err)
		again, err := Import(exported, selected)
		require.NoError(t, err)
		require.Equal(t, suite, again)
	})
}
