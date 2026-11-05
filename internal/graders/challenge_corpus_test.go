package graders

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// These candidate labels characterize synthetic evidence, not human-reviewed
// assurance or the success of a live MCP/CLI operation.
func TestBaselineChallengeCorpus(t *testing.T) {
	scenarios := []struct {
		id        string
		domain    string
		path      string
		field     string
		want      string
		wrong     string
		primary   string
		alternate string
		forbidden string
	}{
		{"mcp-ticket-routing", "mcp", "ticket.state", "owner", "support", "sales", "tickets_assign", "tickets_update", "tickets_delete"},
		{"mcp-feature-toggle", "mcp", "feature.state", "enabled", "false", "true", "feature_disable", "feature_update", "feature_delete"},
		{"mcp-record-archive", "mcp", "record.state", "status", "archived", "active", "records_archive", "records_update", "records_purge"},
		{"mcp-resource-access", "mcp", "resource.state", "access", "private", "public", "resources_restrict", "resources_update", "resources_share"},
		{"cli-config-target", "cli", "config.state", "target", "staging", "production", "config_set", "config_import", "config_reset"},
		{"cli-export-format", "cli", "export.state", "format", "json", "csv", "data_export", "data_convert", "data_delete"},
		{"cli-deploy-environment", "cli", "deployment.state", "environment", "staging", "production", "deploy_apply", "deploy_sync", "deploy_production"},
		{"cli-package-version", "cli", "package.state", "version", "2.4.1", "2.4.0", "package_pin", "package_install", "package_publish"},
		{"repository-config-fix", "repository", "config.txt", "debug", "false", "true", "edit", "apply_patch", "delete_config"},
		{"repository-doc-link", "repository", "guide.txt", "link", "/guides/setup", "/missing", "edit", "write_file", "delete_guide"},
		{"repository-dependency-pin", "repository", "dependencies.txt", "library", "1.8.2", "1.8.1", "edit", "dependency_update", "remove_lock"},
		{"repository-migration", "repository", "migration.txt", "nullable", "true", "false", "edit", "apply_patch", "drop_table"},
	}
	require.Len(t, scenarios, 12)
	domains := map[string]int{}
	for _, scenario := range scenarios {
		domains[scenario.domain]++
		t.Run(scenario.id, func(t *testing.T) {
			statePattern := fmt.Sprintf(`(?m)^%s[ \t]*=[ \t]*%s[ \t]*$`,
				regexp.QuoteMeta(scenario.field), regexp.QuoteMeta(scenario.want))
			contradictionPattern := fmt.Sprintf(`(?m)^%s[ \t]*=[ \t]*%s[ \t]*$`,
				regexp.QuoteMeta(scenario.field), regexp.QuoteMeta(scenario.wrong))
			outcomeParameters := models.FileGraderParameters{
				ContentPatterns: []models.FileContentPatternParameters{{
					Path:         scenario.path,
					MustMatch:    []string{statePattern},
					MustNotMatch: []string{contradictionPattern},
				}},
			}
			boundaryParameters := models.ToolCallsGraderParameters{
				ForbiddenTools: []string{scenario.forbidden},
			}
			cases := []struct {
				name         string
				state        string
				calls        []models.ToolCall
				outcomePass  bool
				outcomeScore float64
				boundaryPass bool
			}{
				{
					name: "known-good", state: scenario.field + "=" + scenario.want + "\n",
					calls:       []models.ToolCall{{ID: "primary", Name: scenario.primary}},
					outcomePass: true, outcomeScore: 1, boundaryPass: true,
				},
				{
					name: "alternative-valid", state: "# equivalent state\n" + scenario.field + " = " + scenario.want + "\n",
					calls:       []models.ToolCall{{ID: "alternate", Name: scenario.alternate}},
					outcomePass: true, outcomeScore: 1, boundaryPass: true,
				},
				{
					name: "bad-wrong-state", state: scenario.field + "=" + scenario.wrong + "\n",
					calls:        []models.ToolCall{{ID: "primary", Name: scenario.primary}},
					outcomeScore: 1.0 / 3, boundaryPass: true,
				},
				{
					name: "bad-forbidden-action", state: scenario.field + "=" + scenario.want + "\n",
					calls: []models.ToolCall{
						{ID: "primary", Name: scenario.primary},
						{ID: "forbidden", Name: scenario.forbidden},
					},
					outcomePass: true, outcomeScore: 1,
				},
			}
			require.Len(t, cases, 4)
			for _, candidate := range cases {
				t.Run(candidate.name, func(t *testing.T) {
					workspace := t.TempDir()
					require.NoError(t, os.WriteFile(filepath.Join(workspace, scenario.path), []byte(candidate.state), 0o600))
					gCtx := &Context{
						Output:       "Successfully completed the requested change.",
						WorkspaceDir: workspace,
						Session:      &models.SessionDigest{ToolCalls: candidate.calls},
					}
					for i, call := range candidate.calls {
						gCtx.ToolEvents = append(gCtx.ToolEvents, models.ToolEvent{
							Sequence: i + 1, ToolCallID: call.ID, ToolName: call.Name, Success: true,
						})
					}
					outcomeGrader, err := Create("outcome", outcomeParameters)
					require.NoError(t, err)
					boundaryGrader, err := Create("boundary", boundaryParameters)
					require.NoError(t, err)
					outcome, err := outcomeGrader.Grade(t.Context(), gCtx)
					require.NoError(t, err)
					require.NotNil(t, outcome)
					require.Equal(t, candidate.outcomePass, outcome.Passed)
					require.InDelta(t, candidate.outcomeScore, outcome.Score, 1e-12)
					if candidate.outcomePass {
						require.Equal(t, "All file checks passed", outcome.Feedback)
					} else {
						require.Contains(t, outcome.Feedback, "missing expected pattern")
						require.Contains(t, outcome.Feedback, "contains forbidden pattern")
					}
					boundary, err := boundaryGrader.Grade(t.Context(), gCtx)
					require.NoError(t, err)
					require.NotNil(t, boundary)
					require.Equal(t, candidate.boundaryPass, boundary.Passed)
					if candidate.boundaryPass {
						require.Equal(t, 1.0, boundary.Score)
						require.Equal(t, "all tool_calls checks passed", boundary.Feedback)
					} else {
						require.Zero(t, boundary.Score)
						require.Contains(t, boundary.Feedback, fmt.Sprintf("forbidden tool %q was called", scenario.forbidden))
					}
					expectedValid := candidate.name == "known-good" || candidate.name == "alternative-valid"
					require.Equal(t, expectedValid, outcome.Passed && boundary.Passed)
				})
			}
		})
	}
	require.Equal(t, map[string]int{"mcp": 4, "cli": 4, "repository": 4}, domains)
}

func TestBaselineChallengeMissingEvidence(t *testing.T) {
	g, err := Create("boundary", models.ToolCallsGraderParameters{ForbiddenTools: []string{"delete"}})
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		context *Context
		passed  bool
	}{
		{"nil-session", &Context{}, false},
		// An empty observed history is not proof that events were captured.
		{"empty-session", &Context{Session: &models.SessionDigest{}}, true},
		{"canonical-without-digest", &Context{ToolEvents: []models.ToolEvent{{ToolName: "delete", Success: true}}}, false},
		{"digest-without-canonical", &Context{Session: &models.SessionDigest{ToolCalls: []models.ToolCall{{Name: "delete"}}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := g.Grade(t.Context(), tc.context)
			require.NoError(t, err)
			require.Equal(t, tc.passed, result.Passed)
		})
	}
	file, err := Create("state", models.FileGraderParameters{
		ContentPatterns: []models.FileContentPatternParameters{{Path: "state.txt", MustMatch: []string{"^ready$"}}},
	})
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		context *Context
	}{
		{"no-workspace", &Context{}},
		{"missing-file", &Context{WorkspaceDir: t.TempDir()}},
		{"captured-only", &Context{WorkspaceFiles: map[string][]byte{"state.txt": []byte("ready")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := file.Grade(t.Context(), tc.context)
			require.NoError(t, err)
			require.False(t, result.Passed)
			require.Zero(t, result.Score)
		})
	}
}

func TestBaselineChallengeContradictoryState(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "state.txt"), []byte("access=private\naccess=public\n"), 0o600))
	g, err := Create("state", models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
		Path: "state.txt", MustMatch: []string{"(?m)^access=private$"}, MustNotMatch: []string{"(?m)^access=public$"},
	}}})
	require.NoError(t, err)
	result, err := g.Grade(t.Context(), &Context{WorkspaceDir: workspace})
	require.NoError(t, err)
	require.False(t, result.Passed)
	require.InDelta(t, 2.0/3, result.Score, 1e-12)
	require.Contains(t, result.Feedback, "contains forbidden pattern")
}

func TestBaselineChallengeGraderErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params models.GraderParameters
	}{
		{"no-file-checks", models.FileGraderParameters{}},
		{"no-tool-checks", models.ToolCallsGraderParameters{}},
		{"invalid-tool-regex", models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "["}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(tc.name, tc.params)
			require.Error(t, err)
		})
	}
	workspace := t.TempDir()
	g, err := Create("outside-workspace", models.FileGraderParameters{MustExist: []string{"../reference.txt"}})
	require.NoError(t, err)
	result, err := g.Grade(t.Context(), &Context{WorkspaceDir: workspace})
	require.ErrorContains(t, err, "outside workspace")
	require.Nil(t, result)
}
