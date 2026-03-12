package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/spf13/cobra"
)

func newGradeCommand() *cobra.Command {
	var (
		taskID      string
		resultsFile string
		workspace   string
		judgeModel  string
		verbose     bool
		outputPath  string
	)

	cmd := &cobra.Command{
		Use:   "grade <eval.yaml>",
		Short: "Run graders against results from `waza run --output` without executing an agent",
		Long: `Grade output with the graders defined in an eval spec.

Takes the JSON output of a previous waza run (produced by waza run --output)
and grades the specified task using the eval's grader configuration.

Example:
  waza run eval.yaml --output results.json
  waza grade eval.yaml --task my-task --results results.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGrade(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], taskID, resultsFile, workspace, judgeModel, outputPath, verbose)
		},
	}

	cmd.Flags().StringVar(&taskID, "task", "", "task ID to grade (omit to grade all tasks)")
	cmd.Flags().StringVar(&resultsFile, "results", "", "path to waza run JSON output")
	cmd.Flags().StringVar(&workspace, "workspace", ".", "agent workspace directory for file-based graders; must point to the agent's actual workspace")
	cmd.Flags().StringVar(&judgeModel, "judge-model", "", "model override for prompt graders")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "write full EvaluationOutcome JSON to file (grading results merged with the original task outcomes)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	_ = cmd.MarkFlagRequired("results")

	return cmd
}

func runGrade(ctx context.Context, w, errW io.Writer, specPath, taskID, resultsFile, workspace, judgeModel, outputFile string, verbose bool) error {
	info, err := os.Stat(workspace)
	if err != nil {
		return fmt.Errorf("--workspace path %q: %w", workspace, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--workspace path %q is not a directory", workspace)
	}

	spec, err := models.LoadBenchmarkSpec(specPath)
	if err != nil {
		return fmt.Errorf("failed to load spec: %w", err)
	}

	allTasks, err := loadGradeTasks(spec, specPath, taskID)
	if err != nil {
		return err
	}
	if len(allTasks) == 0 {
		if taskID != "" {
			return fmt.Errorf("task %q not found in spec or is disabled", taskID)
		}
		return errors.New("no tasks found in spec")
	}

	data, readErr := os.ReadFile(resultsFile)
	if readErr != nil {
		return fmt.Errorf("failed to read results file: %w", readErr)
	}
	var outcome models.EvaluationOutcome
	if jsonErr := json.Unmarshal(data, &outcome); jsonErr != nil {
		return fmt.Errorf("failed to parse results JSON: %w", jsonErr)
	}

	runsByTask := make(map[string][]models.RunResult)
	for _, to := range outcome.TestOutcomes {
		runsByTask[to.TestID] = append([]models.RunResult(nil), to.Runs...)
	}

	effectiveJudgeModel := judgeModel
	if effectiveJudgeModel == "" {
		effectiveJudgeModel = spec.Config.JudgeModel
	}

	taskResults := make(map[string]models.GradeOutcome)
	gradedOutcomes := make([]models.TestOutcome, 0, len(allTasks))
	for _, tc := range allTasks {
		runs, ok := runsByTask[tc.TestID]
		if !ok {
			if taskID != "" {
				return fmt.Errorf("task %q not found in results file", tc.TestID)
			}
			if verbose {
				_, _ = fmt.Fprintf(errW, "warning: task %q not found in results file, skipping\n", tc.TestID)
			}
			continue
		}
		if len(runs) == 0 {
			if taskID != "" {
				return fmt.Errorf("task %q has no runs in results file", tc.TestID)
			}
			if verbose {
				if _, err = fmt.Fprintf(errW, "warning: task %q has no runs in results file, skipping\n", tc.TestID); err != nil {
					return fmt.Errorf("failed to write verbose output: %w", err)
				}
			}
			continue
		}

		result, gradedRuns, gradeErr := gradeTaskRuns(ctx, spec, tc, runs, workspace, effectiveJudgeModel, errW, verbose)
		if gradeErr != nil {
			return fmt.Errorf("grading task %q: %w", tc.TestID, gradeErr)
		}
		taskResults[tc.TestID] = result

		status := models.StatusPassed
		if !result.Passed {
			status = models.StatusFailed
		}
		gradedOutcomes = append(gradedOutcomes, models.TestOutcome{
			TestID:      tc.TestID,
			DisplayName: tc.DisplayName,
			Status:      status,
			Runs:        gradedRuns,
		})
	}

	if len(taskResults) == 0 {
		return errors.New("no tasks were graded: none of the spec's tasks had matching runs in the results file")
	}

	var overallSum float64
	allPassed := true
	for _, r := range taskResults {
		overallSum += r.OverallScore
		if !r.Passed {
			allPassed = false
		}
	}

	output := models.GradeOutcome{
		Passed: allPassed,
		Tasks:  taskResults,
	}
	if len(taskResults) > 0 {
		output.OverallScore = overallSum / float64(len(taskResults))
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("failed to encode results: %w", err)
	}

	if outputFile != "" {
		// Preserve test outcomes that weren't regraded in this run
		gradedIDs := make(map[string]bool)
		for _, to := range gradedOutcomes {
			gradedIDs[to.TestID] = true
		}

		finalOutcomes := append([]models.TestOutcome{}, gradedOutcomes...)
		for _, orig := range outcome.TestOutcomes {
			if !gradedIDs[orig.TestID] {
				finalOutcomes = append(finalOutcomes, orig)
			}
		}

		graded := orchestration.RegradeOutcome(&outcome, finalOutcomes, effectiveJudgeModel)
		if err := saveOutcome(graded, outputFile); err != nil {
			return fmt.Errorf("failed to save graded outcome: %w", err)
		}
	}

	return nil
}

func loadGradeTasks(spec *models.BenchmarkSpec, specPath, taskID string) ([]*models.TestCase, error) {
	all, err := loadTestCases(spec, specPath)
	if err != nil {
		return nil, err
	}

	if taskID == "" {
		return all, nil
	}

	for _, tc := range all {
		if tc.TestID == taskID {
			return []*models.TestCase{tc}, nil
		}
	}
	return nil, nil
}

func gradeTaskRuns(ctx context.Context, spec *models.BenchmarkSpec, tc *models.TestCase, runs []models.RunResult, workspace, judgeModel string, errW io.Writer, verbose bool) (models.GradeOutcome, []models.RunResult, error) {
	totalScore := 0.0
	allPassed := true
	graderWeightedTotals := make(map[string]float64)
	graderWeights := make(map[string]float64)
	gradedRuns := make([]models.RunResult, 0, len(runs))

	for i := range runs {
		gradedRun, err := gradeRun(ctx, spec, tc, &runs[i], workspace, judgeModel, errW, verbose)
		if err != nil {
			return models.GradeOutcome{}, nil, err
		}

		totalScore += gradedRun.ComputeWeightedRunScore()
		if gradedRun.Status != models.StatusPassed {
			allPassed = false
		}

		for name, result := range gradedRun.Validations {
			w := result.Weight
			if w <= 0 {
				w = 1.0
			}
			graderWeightedTotals[name] += result.Score * w
			graderWeights[name] += w
		}
		gradedRuns = append(gradedRuns, *gradedRun)
	}

	graderAverages := make(map[string]float64, len(graderWeightedTotals))
	for name, total := range graderWeightedTotals {
		graderAverages[name] = total / graderWeights[name]
	}

	return models.GradeOutcome{
		OverallScore:   totalScore / float64(len(runs)),
		Passed:         allPassed,
		GraderAverages: graderAverages,
	}, gradedRuns, nil
}

func gradeRun(ctx context.Context, spec *models.BenchmarkSpec, tc *models.TestCase, run *models.RunResult, workspace, judgeModel string, errW io.Writer, verbose bool) (*models.RunResult, error) {
	gradedRun := *run
	if gradedRun.ErrorMsg != "" || gradedRun.Status == models.StatusError {
		gradedRun.Validations = map[string]models.GraderResults{}
		gradedRun.Status = models.StatusError
		return &gradedRun, nil
	}

	skillInvocations := make([]execution.SkillInvocation, len(run.SkillInvocations))
	for i, si := range run.SkillInvocations {
		skillInvocations[i] = execution.SkillInvocation{Name: si.Name, Path: si.Path}
	}

	gradingCtx := &graders.Context{
		TestCase:         tc,
		Output:           run.FinalOutput,
		Transcript:       run.Transcript,
		Session:          &run.SessionDigest,
		DurationMS:       run.DurationMs,
		SessionID:        run.SessionDigest.SessionID,
		WorkspaceDir:     workspace,
		SkillInvocations: skillInvocations,
		Outcome:          make(map[string]any),
		Metadata:         make(map[string]any),
	}

	if verbose {
		if _, err := fmt.Fprintf(errW, "grading %s: %d transcript events, duration=%dms, session=%s\n",
			tc.TestID, len(run.Transcript), run.DurationMs, run.SessionDigest.SessionID); err != nil {
			return nil, fmt.Errorf("failed to write verbose output: %w", err)
		}
	}

	graderResults, err := graders.RunAll(ctx, spec.Graders, tc, gradingCtx, judgeModel, false)
	if err != nil {
		return nil, err
	}

	if verbose {
		for name, result := range graderResults {
			if _, err := fmt.Fprintf(errW, "  grader %s: score=%.2f passed=%v\n", name, result.Score, result.Passed); err != nil {
				return nil, fmt.Errorf("failed to write verbose output: %w", err)
			}
		}
	}

	gradedRun.Validations = graderResults
	gradedRun.Status = models.StatusPassed
	for _, result := range graderResults {
		if !result.Passed {
			gradedRun.Status = models.StatusFailed
			break
		}
	}

	return &gradedRun, nil
}
