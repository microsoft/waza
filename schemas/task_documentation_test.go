package schemas_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDocumentedRequirementTaskValidatesSchemaAndRuntime(t *testing.T) {
	document, err := os.ReadFile("../site/src/content/docs/guides/eval-yaml.mdx")
	require.NoError(t, err)
	lfDocument := strings.ReplaceAll(string(document), "\r\n", "\n")
	for _, format := range []struct {
		name     string
		document string
	}{
		{name: "LF", document: lfDocument},
		{name: "CRLF", document: strings.ReplaceAll(lfDocument, "\n", "\r\n")},
	} {
		t.Run(format.name, func(t *testing.T) {
			text := strings.ReplaceAll(format.document, "\r\n", "\n")
			_, section, found := strings.Cut(text, "### Link intent to existing checks\n")
			require.True(t, found)
			_, snippet, found := strings.Cut(section, "```yaml\n")
			require.True(t, found)
			snippet, _, found = strings.Cut(snippet, "\n```")
			require.True(t, found)

			var schema any
			require.NoError(t, json.Unmarshal([]byte(schemas.TaskSchemaJSON), &schema))
			compiler := jsonschema.NewCompiler()
			compiler.UseLoader(schemaloader.Offline{})
			require.NoError(t, compiler.AddResource("task.json", schema))
			compiled, err := compiler.Compile("task.json")
			require.NoError(t, err)
			var instance any
			require.NoError(t, yaml.Unmarshal([]byte(snippet), &instance))
			require.NoError(t, compiled.Validate(instance), "the copied documentation task must satisfy the actual schema")

			filename := filepath.Join(t.TempDir(), "documented.task.yaml")
			require.NoError(t, os.WriteFile(filename, []byte(snippet), 0o600))
			task, err := models.LoadTestCase(filename)
			require.NoError(t, err)
			require.NoError(t, task.Validate())
			require.Len(t, task.Requirements, 1)
			require.Equal(t, "outcome", task.Requirements[0].Category)
			require.Equal(t, task.Validators[0].Identifier, task.Requirements[0].Checks[0].Grader)

			object, ok := instance.(map[string]any)
			require.True(t, ok)
			requirements, ok := object["requirements"].([]any)
			require.True(t, ok)
			requirement, ok := requirements[0].(map[string]any)
			require.True(t, ok)
			requirement["category"] = "output"
			require.Error(t, compiled.Validate(instance), "the previous copied example must fail real schema admission")
		})
	}
}
