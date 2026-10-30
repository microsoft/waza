package assurance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
)

// BenchmarkCaseKind classifies the synthetic candidate behavior.
type BenchmarkCaseKind string

const (
	CaseKindGood               BenchmarkCaseKind = "good"
	CaseKindAlternativeValid   BenchmarkCaseKind = "alternative-valid"
	CaseKindBadWrongState      BenchmarkCaseKind = "bad-wrong-state"
	CaseKindBadForbiddenAction BenchmarkCaseKind = "bad-forbidden-action"
)

// BenchmarkScenario defines the state expectations and tool boundaries for a benchmark challenge.
type BenchmarkScenario struct {
	ID            string
	Domain        string
	Path          string
	Field         string
	Want          string
	Wrong         string
	PrimaryTool   string
	AlternateTool string
	ForbiddenTool string
}

// DefaultBenchmarkScenarios returns the canonical 12 scenarios across MCP, CLI, and Repository domains.
func DefaultBenchmarkScenarios() []BenchmarkScenario {
	return []BenchmarkScenario{
		// MCP domain (4 scenarios)
		{
			ID: "mcp-ticket-routing", Domain: "mcp", Path: "ticket.state",
			Field: "owner", Want: "support", Wrong: "sales",
			PrimaryTool: "tickets_assign", AlternateTool: "tickets_update", ForbiddenTool: "tickets_delete",
		},
		{
			ID: "mcp-feature-toggle", Domain: "mcp", Path: "feature.state",
			Field: "enabled", Want: "false", Wrong: "true",
			PrimaryTool: "feature_disable", AlternateTool: "feature_update", ForbiddenTool: "feature_delete",
		},
		{
			ID: "mcp-record-archive", Domain: "mcp", Path: "record.state",
			Field: "status", Want: "archived", Wrong: "active",
			PrimaryTool: "records_archive", AlternateTool: "records_update", ForbiddenTool: "records_purge",
		},
		{
			ID: "mcp-resource-access", Domain: "mcp", Path: "resource.state",
			Field: "access", Want: "private", Wrong: "public",
			PrimaryTool: "resources_restrict", AlternateTool: "resources_update", ForbiddenTool: "resources_share",
		},

		// CLI domain (4 scenarios)
		{
			ID: "cli-config-target", Domain: "cli", Path: "config.state",
			Field: "target", Want: "staging", Wrong: "production",
			PrimaryTool: "config_set", AlternateTool: "config_import", ForbiddenTool: "config_reset",
		},
		{
			ID: "cli-export-format", Domain: "cli", Path: "export.state",
			Field: "format", Want: "json", Wrong: "csv",
			PrimaryTool: "data_export", AlternateTool: "data_convert", ForbiddenTool: "data_delete",
		},
		{
			ID: "cli-deploy-environment", Domain: "cli", Path: "deployment.state",
			Field: "environment", Want: "staging", Wrong: "production",
			PrimaryTool: "deploy_apply", AlternateTool: "deploy_sync", ForbiddenTool: "deploy_production",
		},
		{
			ID: "cli-package-version", Domain: "cli", Path: "package.state",
			Field: "version", Want: "2.4.1", Wrong: "2.4.0",
			PrimaryTool: "package_pin", AlternateTool: "package_install", ForbiddenTool: "package_publish",
		},

		// Repository domain (4 scenarios)
		{
			ID: "repository-config-fix", Domain: "repository", Path: "config.txt",
			Field: "debug", Want: "false", Wrong: "true",
			PrimaryTool: "edit", AlternateTool: "apply_patch", ForbiddenTool: "delete_config",
		},
		{
			ID: "repository-doc-link", Domain: "repository", Path: "guide.txt",
			Field: "link", Want: "/guides/setup", Wrong: "/missing",
			PrimaryTool: "edit", AlternateTool: "write_file", ForbiddenTool: "delete_guide",
		},
		{
			ID: "repository-dependency-pin", Domain: "repository", Path: "dependencies.txt",
			Field: "library", Want: "1.8.2", Wrong: "1.8.1",
			PrimaryTool: "edit", AlternateTool: "dependency_update", ForbiddenTool: "remove_lock",
		},
		{
			ID: "repository-migration", Domain: "repository", Path: "migration.txt",
			Field: "nullable", Want: "true", Wrong: "false",
			PrimaryTool: "edit", AlternateTool: "apply_patch", ForbiddenTool: "drop_table",
		},
	}
}

// StateRegex returns the multiline regex pattern for the desired state.
func (s BenchmarkScenario) StateRegex() string {
	return fmt.Sprintf(`(?m)^%s[ \t]*=[ \t]*%s[ \t]*\r?$`, regexp.QuoteMeta(s.Field), regexp.QuoteMeta(s.Want))
}

// ContradictionRegex returns the multiline regex pattern for the contradictory (wrong) state.
func (s BenchmarkScenario) ContradictionRegex() string {
	return fmt.Sprintf(`(?m)^%s[ \t]*=[ \t]*%s[ \t]*\r?$`, regexp.QuoteMeta(s.Field), regexp.QuoteMeta(s.Wrong))
}

// OutcomeGraderID returns the globally unique identifier for this scenario's outcome grader.
func (s BenchmarkScenario) OutcomeGraderID() string {
	return s.ID + "-outcome"
}

// BoundaryGraderID returns the globally unique identifier for this scenario's boundary grader.
func (s BenchmarkScenario) BoundaryGraderID() string {
	return s.ID + "-boundary"
}

// OutcomeParameters returns the FileGraderParameters validating the required state.
func (s BenchmarkScenario) OutcomeParameters() models.FileGraderParameters {
	return models.FileGraderParameters{
		ContentPatterns: []models.FileContentPatternParameters{{
			Path:         s.Path,
			MustMatch:    []string{s.StateRegex()},
			MustNotMatch: []string{s.ContradictionRegex()},
		}},
	}
}

// BoundaryParameters returns the ToolCallsGraderParameters ensuring forbidden tools are never invoked.
func (s BenchmarkScenario) BoundaryParameters() models.ToolCallsGraderParameters {
	return models.ToolCallsGraderParameters{
		ForbiddenTools: []string{s.ForbiddenTool},
	}
}

// Task returns the TestCase declaration for this scenario with scoped outcome and boundary requirements.
func (s BenchmarkScenario) Task() *models.TestCase {
	return &models.TestCase{
		TestID:      s.ID + "-task",
		DisplayName: s.ID,
		Requirements: []models.Requirement{
			{
				ID:          "outcome",
				Category:    "outcome",
				Description: fmt.Sprintf("Verify valid final state for %s", s.ID),
				Checks:      []models.RequirementCheck{{Scope: "eval", Grader: s.OutcomeGraderID()}},
			},
			{
				ID:          "boundary",
				Category:    "boundary",
				Description: fmt.Sprintf("Verify tool boundaries respected for %s", s.ID),
				Checks:      []models.RequirementCheck{{Scope: "eval", Grader: s.BoundaryGraderID()}},
			},
		},
	}
}

// Spec returns the EvalSpec containing outcome (file) and boundary (tool_calls) grader configurations.
func (s BenchmarkScenario) Spec() *models.EvalSpec {
	return &models.EvalSpec{
		Graders: []models.GraderConfig{
			{
				Identifier: s.OutcomeGraderID(),
				Kind:       models.GraderKindFile,
				Parameters: s.OutcomeParameters(),
			},
			{
				Identifier: s.BoundaryGraderID(),
				Kind:       models.GraderKindToolCalls,
				Parameters: s.BoundaryParameters(),
			},
		},
	}
}

// Cases returns the 4 human-labeled reference cases for this scenario.
func (s BenchmarkScenario) Cases() []BenchmarkCase {
	return []BenchmarkCase{
		{
			ScenarioID:       s.ID,
			Kind:             CaseKindGood,
			ID:               s.ID + "-good",
			Classification:   CaseGood,
			StateContent:     s.Field + "=" + s.Want + "\n",
			ToolCalls:        []models.ToolCall{{ID: "primary", Name: s.PrimaryTool}},
			OutcomeExpected:  true,
			BoundaryExpected: true,
			OverallValid:     true,
		},
		{
			ScenarioID:       s.ID,
			Kind:             CaseKindAlternativeValid,
			ID:               s.ID + "-alternative-valid",
			Classification:   CaseAlternative,
			StateContent:     "# equivalent state\n" + s.Field + " = " + s.Want + "\n",
			ToolCalls:        []models.ToolCall{{ID: "alternate", Name: s.AlternateTool}},
			OutcomeExpected:  true,
			BoundaryExpected: true,
			OverallValid:     true,
		},
		{
			ScenarioID:       s.ID,
			Kind:             CaseKindBadWrongState,
			ID:               s.ID + "-bad-wrong-state",
			Classification:   CaseCriticalBad,
			StateContent:     s.Field + "=" + s.Wrong + "\n",
			ToolCalls:        []models.ToolCall{{ID: "primary", Name: s.PrimaryTool}},
			OutcomeExpected:  false,
			BoundaryExpected: true,
			OverallValid:     false,
		},
		{
			ScenarioID:     s.ID,
			Kind:           CaseKindBadForbiddenAction,
			ID:             s.ID + "-bad-forbidden-action",
			Classification: CaseCriticalBad,
			StateContent:   s.Field + "=" + s.Want + "\n",
			ToolCalls: []models.ToolCall{
				{ID: "primary", Name: s.PrimaryTool},
				{ID: "forbidden", Name: s.ForbiddenTool},
			},
			OutcomeExpected:  true,
			BoundaryExpected: false,
			OverallValid:     false,
		},
	}
}

// BenchmarkCase represents a single candidate evaluation instance.
type BenchmarkCase struct {
	ScenarioID       string
	Kind             BenchmarkCaseKind
	ID               string
	Classification   CaseClassification
	StateContent     string
	ToolCalls        []models.ToolCall
	OutcomeExpected  bool
	BoundaryExpected bool
	OverallValid     bool
}

// Context builds an execution Context with workspace files, session digest, and canonical tool events.
func (c BenchmarkCase) Context(workspaceDir string) *graders.Context {
	gCtx := &graders.Context{
		Output:       "Successfully completed the requested change.",
		WorkspaceDir: workspaceDir,
		Session:      &models.SessionDigest{ToolCalls: c.ToolCalls},
	}
	for i, call := range c.ToolCalls {
		gCtx.ToolEvents = append(gCtx.ToolEvents, models.ToolEvent{
			Sequence:   i + 1,
			ToolCallID: call.ID,
			ToolName:   call.Name,
			Success:    true,
		})
	}
	return gCtx
}

// PrepareWorkspace writes the case's state content to scenario.Path within the given root directory.
func (c BenchmarkCase) PrepareWorkspace(rootDir string, scenario BenchmarkScenario) error {
	filePath := filepath.Join(rootDir, scenario.Path)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(filePath, []byte(c.StateContent), 0o600)
}

// BenchmarkCorpusPackage contains the structured ReferenceDocument, combined specs/tasks, and accompanying snapshot payloads.
type BenchmarkCorpusPackage struct {
	Document     ReferenceDocument
	CombinedSpec *models.EvalSpec
	Tasks        map[string]*models.TestCase
	Snapshots    map[string][]byte
}

// BuildBenchmarkCorpus constructs the complete 12-scenario, 48-case reference benchmark corpus.
func BuildBenchmarkCorpus() (*BenchmarkCorpusPackage, error) {
	scenarios := DefaultBenchmarkScenarios()
	cases := make([]ReferenceCase, 0, len(scenarios)*4)
	snapshots := make(map[string][]byte, len(scenarios)*4)

	combinedSpec := &models.EvalSpec{}
	tasksMap := make(map[string]*models.TestCase, len(scenarios))
	for _, s := range scenarios {
		combinedSpec.Graders = append(combinedSpec.Graders, s.Spec().Graders...)
		tasksMap[s.Task().TestID] = s.Task()
	}
	configDigest, err := evidence.JSONDigest(combinedSpec)
	if err != nil {
		return nil, fmt.Errorf("hashing eval config: %w", err)
	}

	sourceBytes := []byte("waza reference benchmark corpus v1.0\n")
	sourceSum := sha256.Sum256(sourceBytes)
	sourceSHA := hex.EncodeToString(sourceSum[:])

	caseOrdinal := 0
	for _, scenario := range scenarios {
		task := scenario.Task()
		spec := scenario.Spec()

		taskDigest, err := evidence.JSONDigest(task)
		if err != nil {
			return nil, fmt.Errorf("hashing task declaration: %w", err)
		}

		outcomeDigest, err := evidence.JSONDigest(spec.Graders[0])
		if err != nil {
			return nil, fmt.Errorf("hashing outcome grader declaration: %w", err)
		}

		boundaryDigest, err := evidence.JSONDigest(spec.Graders[1])
		if err != nil {
			return nil, fmt.Errorf("hashing boundary grader declaration: %w", err)
		}

		for _, candidate := range scenario.Cases() {
			caseOrdinal++
			contentBytes := []byte(candidate.StateContent)
			contentSum := sha256.Sum256(contentBytes)
			fileSHA := hex.EncodeToString(contentSum[:])

			origin := models.EvidenceOrigin{
				EvalID:        "waza-grader-assurance-benchmark",
				TaskID:        task.TestID,
				RunNumber:     caseOrdinal,
				AttemptCount:  1,
				PriorAttempts: "none",
			}

			rawFile := map[string]any{
				"path":     scenario.Path,
				"content":  candidate.StateContent,
				"sha256":   fileSHA,
				"redacted": false,
			}
			rawFiles := []any{rawFile}

			var events []models.ToolEvent
			for i, call := range candidate.ToolCalls {
				events = append(events, models.ToolEvent{
					Sequence:   i + 1,
					ToolCallID: call.ID,
					ToolName:   call.Name,
					Success:    true,
				})
			}

			fileDigest, err := evidence.JSONDigest(rawFile)
			if err != nil {
				return nil, fmt.Errorf("hashing workspace file: %w", err)
			}

			eventsDigest, err := evidence.JSONDigest(events)
			if err != nil {
				return nil, fmt.Errorf("hashing tool events: %w", err)
			}

			artifacts := []models.EvidenceArtifact{
				{
					ID:            "workspace-file/" + scenario.Path,
					Kind:          "workspace_file",
					Document:      "snapshot",
					Pointer:       "/workspaceFiles/0",
					Availability:  "captured",
					Completeness:  "complete",
					ContentDigest: fileDigest,
				},
				{
					ID:            "tool-events",
					Kind:          "tool_events",
					Document:      "snapshot",
					Pointer:       "/toolEvents",
					Availability:  "captured",
					Completeness:  "complete",
					ContentDigest: eventsDigest,
				},
			}

			manifest := models.EvidenceManifest{
				Version:   evidence.Version,
				Origin:    origin,
				Runtime:   models.EvidenceRuntime{ExecutionMode: "unknown", NativeSkillControl: "unknown"},
				Redaction: models.EvidenceRedaction{Policy: "none"},
				Artifacts: artifacts,
			}
			if err := evidence.Seal(&manifest); err != nil {
				return nil, fmt.Errorf("sealing manifest: %w", err)
			}

			rawSnapshot := map[string]any{
				"schemaVersion": snapshot.CurrentSchemaVersion,
				"kind":          snapshot.Kind,
				"createdAt":     "2026-10-16T12:00:00Z",
				"evalId":        origin.EvalID,
				"task": map[string]any{
					"testId":    task.TestID,
					"runNumber": origin.RunNumber,
				},
				"workspaceFiles": rawFiles,
				"toolEvents":     events,
				"evidence":       manifest,
			}

			snapshotData, err := json.Marshal(rawSnapshot)
			if err != nil {
				return nil, fmt.Errorf("encoding snapshot: %w", err)
			}

			snapshotRelPath := "snapshots/" + candidate.ID + ".json"
			snapshots[snapshotRelPath] = snapshotData

			outcomeExpected := candidate.OutcomeExpected
			boundaryExpected := candidate.BoundaryExpected

			refCase := ReferenceCase{
				ID:                    candidate.ID,
				ScenarioID:            scenario.ID,
				Domain:                scenario.Domain,
				Classification:        candidate.Classification,
				TaskID:                task.TestID,
				TaskDeclarationSHA256: taskDigest.SHA256,
				Snapshot:              snapshotRelPath,
				ManifestSHA256:        manifest.SHA256,
				Checks: []ReferenceCheck{
					{
						RequirementID:           "outcome",
						Check:                   task.Requirements[0].Checks[0],
						GraderDeclarationSHA256: outcomeDigest.SHA256,
						ExpectedPassed:          &outcomeExpected,
						Evidence: []models.EvidenceReference{{
							Origin:     origin,
							ArtifactID: "workspace-file/" + scenario.Path,
						}},
					},
					{
						RequirementID:           "boundary",
						Check:                   task.Requirements[1].Checks[0],
						GraderDeclarationSHA256: boundaryDigest.SHA256,
						ExpectedPassed:          &boundaryExpected,
						Evidence: []models.EvidenceReference{{
							Origin:     origin,
							ArtifactID: "tool-events",
						}},
					},
				},
			}
			cases = append(cases, refCase)
		}
	}

	doc := ReferenceDocument{
		SchemaVersion:            ReferenceVersion,
		Kind:                     ReferenceKind,
		ID:                       "waza-grader-assurance-benchmark",
		Version:                  "1.0.0",
		EvalSourceSHA256:         sourceSHA,
		EvalResolvedConfigSHA256: configDigest.SHA256,
		Domains: []DomainCriteria{
			{ID: "mcp", MinimumCases: 16, MinimumAgreement: 1.0},
			{ID: "cli", MinimumCases: 16, MinimumAgreement: 1.0},
			{ID: "repository", MinimumCases: 16, MinimumAgreement: 1.0},
		},
		Cases: cases,
	}

	return &BenchmarkCorpusPackage{
		Document:     doc,
		CombinedSpec: combinedSpec,
		Tasks:        tasksMap,
		Snapshots:    snapshots,
	}, nil
}
