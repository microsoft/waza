package orchestration

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRequiredSkillDiscoverySkipsMalformedDefinitions(t *testing.T) {
	for _, scenario := range []string{"", "inventory"} {
		for _, filename := range []string{"SKILL.md", "broken.agent.md"} {
			for _, reverse := range []bool{false, true} {
				for _, missing := range []bool{false, true} {
					t.Run(fmt.Sprintf("scenario=%s/%s/reverse=%t/missing=%t", scenario, filename, reverse, missing), func(t *testing.T) {
						root := t.TempDir()
						good := filepath.Join(root, "good")
						bad := filepath.Join(root, "bad")
						require.NoError(t, os.MkdirAll(good, 0o700))
						require.NoError(t, os.MkdirAll(bad, 0o700))
						name := "wanted"
						if missing {
							name = "other"
						}
						require.NoError(t, os.WriteFile(filepath.Join(good, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Valid definition\n---\n"), 0o600))
						malformed := []byte("---\nname: [secret-definition-payload\n---\n")
						require.NoError(t, os.WriteFile(filepath.Join(bad, filename), malformed, 0o600))
						paths := []string{good, bad}
						if reverse {
							paths[0], paths[1] = paths[1], paths[0]
						}
						spec := &models.EvalSpec{Scenario: scenario, Config: models.Config{
							SkillPaths: paths, RequiredSkills: []string{"wanted"},
						}}
						runtimeErr := NewEvalRunner(config.NewEvalConfig(spec, config.WithSpecDir(root)), nil).validateRequiredSkills()
						var warnings bytes.Buffer
						previous := slog.Default()
						slog.SetDefault(slog.New(slog.NewJSONHandler(&warnings, nil)))
						t.Cleanup(func() { slog.SetDefault(previous) })
						offlineErr := ValidateRequiredSkillConfiguration(spec, root)
						if missing {
							require.ErrorContains(t, runtimeErr, "required skills not found")
							require.ErrorContains(t, offlineErr, "required skills not found")
							require.ErrorContains(t, offlineErr, "wanted")
						} else {
							require.NoError(t, runtimeErr)
							require.NoError(t, offlineErr)
						}
						require.Contains(t, warnings.String(), "skipping malformed definition")
						require.Contains(t, warnings.String(), "WARN")
						require.NotContains(t, warnings.String(), "secret-definition-payload")
						unchanged, err := os.ReadFile(filepath.Join(bad, filename))
						require.NoError(t, err)
						require.Equal(t, malformed, unchanged)
					})
				}
			}
		}
	}
}
