package orchestration

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/microsoft/waza/internal/dataset"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/models"
)

// LoadSelectedTestCases loads the active tasks execution would select from an
// eval spec before command-level task and tag filters are applied.
func LoadSelectedTestCases(spec *models.EvalSpec, specDir string, now time.Time) ([]*models.TestCase, error) {
	return loadSelectedTestCases(spec, specDir, now, "", models.LoadTestCase, mcpmock.ValidateInlineMockDeclarations)
}

// LoadSelectedTestCasesWithJobID loads selected tasks using the allocated ID
// when rendering runtime CSV prompts.
func LoadSelectedTestCasesWithJobID(spec *models.EvalSpec, specDir string, now time.Time, jobID string) ([]*models.TestCase, error) {
	return loadSelectedTestCases(spec, specDir, now, jobID, models.LoadTestCase, mcpmock.ValidateInlineMockDeclarations)
}

// LoadSelectedTestCasesOffline uses execution's task selection while refusing
// external schema resolution during task decoding and MCP validation.
func LoadSelectedTestCasesOffline(spec *models.EvalSpec, specDir string, now time.Time) ([]*models.TestCase, error) {
	load := func(path string) (*models.TestCase, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return models.ParseTestCaseOffline(data, path, graders.ValidateConfig)
	}
	return loadSelectedTestCases(spec, specDir, now, "", load, mcpmock.ValidateInlineMockDeclarationsOffline)
}

type testCaseLoader func(string) (*models.TestCase, error)
type inlineMockValidator func([]models.MCPMockConfig, string, string) error

func loadSelectedTestCases(
	spec *models.EvalSpec,
	specDir string,
	now time.Time,
	jobID string,
	load testCaseLoader,
	validateInlineMocks inlineMockValidator,
) ([]*models.TestCase, error) {
	if spec.TasksFrom != "" {
		return loadSelectedTestCasesFromCSV(spec, specDir, now, jobID)
	}

	if specDir == "" {
		specDir = "."
	}

	testFiles := make([]string, 0)
	for _, pattern := range spec.Tasks {
		matches, err := filepath.Glob(filepath.Join(specDir, pattern))
		if err != nil {
			return nil, err
		}
		testFiles = append(testFiles, matches...)
	}
	if len(testFiles) == 0 {
		return nil, fmt.Errorf("no test files matched patterns: %v in directory: %s", spec.Tasks, specDir)
	}

	testCases := make([]*models.TestCase, 0, len(testFiles))
	for _, path := range testFiles {
		tc, err := load(path)
		if err != nil {
			return nil, fmt.Errorf("failed to load test case %s: %w", path, err)
		}
		if err := models.ValidateTaskFaultSources(tc.SourceBytes(), spec.SchemaVersion); err != nil {
			return nil, fmt.Errorf("invalid test case %s: task fault sources: %w", path, err)
		}
		if err := tc.ValidateForExecutor(spec.Config.EngineType); err != nil {
			return nil, fmt.Errorf("invalid test case %s: %w", path, err)
		}
		if tc.CommandMocks != nil {
			if err := models.ValidateCommandMocksSchemaVersion(spec.SchemaVersion); err != nil {
				return nil, fmt.Errorf("invalid test case %s: %w", path, err)
			}
		}
		if len(tc.MCPMocks) > 0 {
			if err := models.ValidateMCPMocksSchemaVersion(spec); err != nil {
				return nil, fmt.Errorf("invalid test case %s: %w", path, err)
			}
			if err := validateInlineMocks(tc.MCPMocks, specDir, spec.SchemaVersion); err != nil {
				return nil, fmt.Errorf("invalid test case %s: MCP mock admission: %w", path, err)
			}
		}
		if tc.Active == nil || *tc.Active {
			testCases = append(testCases, tc)
		}
	}

	return testCases, nil
}

func loadSelectedTestCasesFromCSV(spec *models.EvalSpec, specDir string, now time.Time, jobID string) ([]*models.TestCase, error) {
	if jobID == "" {
		return dataset.LoadTasks(spec, specDir, now)
	}
	return dataset.LoadTasksWithJobID(spec, specDir, now, jobID)
}
