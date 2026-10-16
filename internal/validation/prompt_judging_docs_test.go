package validation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEvidenceJudgeExampleAndCanonicalGuideUseActualLoaderAndSchema(t *testing.T) {
	root := filepath.Join("..", "..")
	evalPath := filepath.Join(root, "examples", "evidence-judge", "eval.yaml")
	evalErrors, taskErrors, err := ValidateEvalFile(evalPath)
	require.NoError(t, err)
	require.Empty(t, evalErrors)
	require.Empty(t, taskErrors)
	for _, diagnostics := range taskErrors {
		require.Empty(t, diagnostics)
	}
	spec, err := models.LoadEvalSpec(evalPath)
	require.NoError(t, err)
	require.Len(t, spec.Graders, 1)
	require.Equal(t, []string{"tasks/report.yaml"}, spec.Tasks)
	taskData, err := os.ReadFile(filepath.Join(filepath.Dir(evalPath), spec.Tasks[0]))
	require.NoError(t, err)
	require.Empty(t, ValidateTaskBytes(taskData), "the real referenced task must be checked")
	_, err = models.LoadTestCase(filepath.Join(root, "examples", "evidence-judge", "tasks", "report.yaml"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "examples", "evidence-judge", "fixtures", "report.json"))
	require.NoError(t, err)
	guide, err := os.ReadFile(filepath.Join(root, "site", "src", "content", "docs", "guides", "graders.mdx"))
	require.NoError(t, err)
	_, section, ok := strings.Cut(string(guide), "### Evidence-backed independent judging")
	require.True(t, ok)
	_, section, ok = strings.Cut(section, "```yaml\n")
	require.True(t, ok)
	snippet, _, ok := strings.Cut(section, "```")
	require.True(t, ok)
	var copied []models.GraderConfig
	require.NoError(t, yaml.Unmarshal([]byte(snippet), &copied))
	require.Equal(t, spec.Graders, copied, "canonical public YAML must match the runnable example")
	raw, err := os.ReadFile(evalPath)
	require.NoError(t, err)
	var document map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &document))
	var graderNodes []any
	require.NoError(t, yaml.Unmarshal([]byte(snippet), &graderNodes))
	document["graders"] = graderNodes
	encoded, err := yaml.Marshal(document)
	require.NoError(t, err)
	require.Empty(t, ValidateEvalBytes(encoded))
	_, err = models.ParseEvalSpec(encoded, "canonical-guide.yaml")
	require.NoError(t, err)
}
