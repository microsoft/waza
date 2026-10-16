package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/spf13/cobra"
)

func newGradeCommand() *cobra.Command {
	var (
		taskID           string
		resultsFile      string
		workspace        string
		judgeModel       string
		verbose          bool
		outputPath       string
		evidenceSnapshot string
	)

	cmd := &cobra.Command{
		Use:   "grade <eval.yaml>",
		Short: "Run graders against results from `waza run --output` without executing an agent",
		Long: `Grade output with the graders defined in an eval spec.

Takes the JSON output of a previous waza run (produced by waza run --output)
and grades the specified task using the eval's grader configuration.
File-based graders require the agent's actual preserved workspace via
--workspace; snapshot hashes and tool events cannot reconstruct those files.
Prompt graders may contact the configured judge model.
Final prompt graders with config.evidence_context use the persisted frozen
judge_evidence bundle in a fresh isolated judge session, without resuming the
actor or following snapshot_path. Evidence-only grading does not inspect
--workspace. Do not combine it with legacy prompt graders or --evidence-snapshot.
New judge usage is recorded separately as regrade_usage; original billing stays
unchanged. Ordinary failed judgments retain exit 0; operational errors return
nonzero and omit overall_score in the stdout summary.
--evidence-snapshot is available only on Linux/macOS and supports verified file
checks without output-based expectations; it does not certify absence from the
original workspace.

Example:
  waza run eval.yaml --output results.json
  waza grade eval.yaml --task my-task --results results.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if evidenceSnapshot != "" && cmd.Flags().Changed("workspace") {
				return fmt.Errorf("--evidence-snapshot and --workspace cannot be combined")
			}
			if evidenceSnapshot == "" {
				return runGrade(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], taskID, resultsFile, workspace, judgeModel, outputPath, verbose)
			}
			return runGradeWithEvidence(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], taskID, resultsFile, workspace, judgeModel, outputPath, verbose, evidenceSnapshot)
		},
	}

	cmd.Flags().StringVar(&taskID, "task", "", "task ID to grade (omit to grade all tasks)")
	cmd.Flags().StringVar(&resultsFile, "results", "", "path to waza run JSON output")
	cmd.Flags().StringVar(&workspace, "workspace", ".", "agent workspace directory for file-based graders; must point to the agent's actual workspace")
	cmd.Flags().StringVar(&evidenceSnapshot, "evidence-snapshot", "", "Explicit local snapshot for verified file-only checks on Linux/macOS, without output-based expectations; requires --task and exactly one run")
	cmd.Flags().StringVar(&judgeModel, "judge-model", "", "model override for prompt graders")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "write full EvaluationOutcome JSON to file (grading results merged with the original task outcomes)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")
	_ = cmd.MarkFlagRequired("results")

	return cmd
}

func runGrade(ctx context.Context, w, errW io.Writer, specPath, taskID, resultsFile, workspace, judgeModel, outputFile string, verbose bool) error {
	return runGradeWithEvidence(ctx, w, errW, specPath, taskID, resultsFile, workspace, judgeModel, outputFile, verbose, "")
}

func runGradeWithEvidence(ctx context.Context, w, errW io.Writer, specPath, taskID, resultsFile, workspace, judgeModel, outputFile string, verbose bool, evidenceSnapshot string) error {
	return runGradeWithJudge(ctx, w, errW, specPath, taskID, resultsFile, workspace, judgeModel, outputFile, verbose, evidenceSnapshot, nil)
}

func runGradeWithJudge(ctx context.Context, w, errW io.Writer, specPath, taskID, resultsFile, workspace, judgeModel, outputFile string, verbose bool, evidenceSnapshot string, judgeExecutor graders.Executor) (resultErr error) {
	if evidenceSnapshot != "" && taskID == "" {
		return fmt.Errorf("--evidence-snapshot requires --task")
	}
	spec, err := models.LoadEvalSpec(specPath)
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
	evidenceRequested, evidenceOnly := false, true
	for _, task := range allTasks {
		evidenceRequested = evidenceRequested || models.UsesJudgeEvidence(spec.Graders, task)
		evidenceOnly = evidenceOnly && onlyEvidenceGraders(spec.Graders, task)
	}
	if evidenceRequested && evidenceSnapshot != "" {
		return errors.New("evidence_context and --evidence-snapshot cannot be combined; judge the persisted run bundle without a snapshot")
	}
	if !evidenceOnly {
		info, err := os.Stat(workspace)
		if err != nil {
			return fmt.Errorf("--workspace path %q: %w", workspace, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("--workspace path %q is not a directory", workspace)
		}
	}
	var regradeScope *execution.UsageScope
	if evidenceRequested {
		ctx, regradeScope = execution.NewUsageScope(ctx)
	}

	data, readErr := os.ReadFile(resultsFile)
	if readErr != nil {
		return fmt.Errorf("failed to read results file: %w", readErr)
	}
	if evidenceSnapshot != "" {
		if _, err := jsonutil.Parse(data); err != nil {
			return fmt.Errorf("preserved-state results JSON: %w", err)
		}
	}
	outcome, jsonErr := models.ParseEvaluationOutcome(data, resultsFile)
	if jsonErr != nil {
		return fmt.Errorf("failed to parse results JSON: %w", jsonErr)
	}

	runsByTask := make(map[string][]models.RunResult)
	cachedByTask := make(map[string]bool)
	for _, to := range outcome.TestOutcomes {
		if _, exists := runsByTask[to.TestID]; exists {
			return fmt.Errorf("results file contains duplicate task ID %q", to.TestID)
		}
		runsByTask[to.TestID] = append([]models.RunResult(nil), to.Runs...)
		cachedByTask[to.TestID] = to.Cached
	}

	for _, tc := range allTasks {
		for _, run := range runsByTask[tc.TestID] {
			if run.Evidence != nil && !models.UsesJudgeEvidence(spec.Graders, tc) {
				if err := evidence.Bind(run.Evidence, outcome.RunID, tc.TestID, run.RunNumber, run.Attempts, cachedByTask[tc.TestID]); err != nil {
					return fmt.Errorf("grading task %q run %d original evidence: %w", tc.TestID, run.RunNumber, err)
				}
			}
		}
	}

	effectiveJudgeModel := judgeModel
	if effectiveJudgeModel == "" {
		effectiveJudgeModel = spec.Config.JudgeModel
	}
	judgeReasoningEffort := spec.Config.JudgeReasoningEffort

	taskResults := make(map[string]models.GradeOutcome)
	gradedOutcomes := make([]models.TestOutcome, 0, len(allTasks))
	var gradeErrors []error
	evidenceAttempted := false
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

		if evidenceSnapshot != "" {
			if len(runs) != 1 || runs[0].Evidence == nil {
				return fmt.Errorf("preserved-state grading requires exactly one run with assessed evidence")
			}
			snap, err := snapshot.LoadSnapshotFile(evidenceSnapshot)
			if err != nil {
				return err
			}
			required, err := preservedGradeInputs(spec, tc)
			if err != nil {
				return err
			}
			manifestSHA := runs[0].Evidence.SHA256
			if runs[0].Evidence.SourceManifestSHA256 != "" {
				manifestSHA = runs[0].Evidence.SourceManifestSHA256
			}
			materialized, err := snapshot.MaterializeWorkspace(snap, runs[0].Evidence.Origin, manifestSHA, required)
			if err != nil {
				return fmt.Errorf("preserved-state grading: %w", err)
			}
			defer func() { resultErr = errors.Join(resultErr, materialized.Close()) }()
			workspace = materialized.Path()
		}

		usesEvidence := models.UsesJudgeEvidence(spec.Graders, tc)
		evidenceAttempted = evidenceAttempted || usesEvidence
		result, gradedRuns, gradeErr := gradeTaskRunsWithJudge(ctx, spec, tc, runs, workspace, effectiveJudgeModel, errW, verbose,
			evidenceGradeSource{EvalID: outcome.RunID, Cached: cachedByTask[tc.TestID], Executor: judgeExecutor})
		if gradeErr != nil {
			if !evidenceAttempted {
				return fmt.Errorf("grading task %q: %w", tc.TestID, gradeErr)
			}
			gradeErrors = append(gradeErrors, fmt.Errorf("grading task %q: %w", tc.TestID, gradeErr))
		}
		if evidenceRequested {
			result.Status = models.StatusPassed
			if gradeErr != nil {
				result.Status = models.StatusError
				result.Error = "Task grading failed; no complete verdict is available."
			} else if !result.Passed {
				result.Status = models.StatusFailed
			}
		}
		taskResults[tc.TestID] = result

		status := models.StatusPassed
		if !result.Passed {
			status = models.StatusFailed
		}
		if gradeErr != nil {
			status = models.StatusError
		}
		cachedEvidence := cachedByTask[tc.TestID] && slices.ContainsFunc(gradedRuns, func(run models.RunResult) bool {
			return run.Evidence != nil || run.JudgeEvidence != nil || len(run.PromptJudging) > 0
		})
		gradedOutcomes = append(gradedOutcomes, models.TestOutcome{
			TestID:      tc.TestID,
			DisplayName: tc.DisplayName,
			Status:      status,
			Runs:        gradedRuns,
			Cached:      cachedEvidence,
		})
		if gradeErr != nil {
			failure, graderFailed := errors.AsType[*graders.RunError](gradeErr)
			if !usesEvidence || !graderFailed || failure.Kind != models.GraderKindPrompt {
				break
			}
		}
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
	if evidenceRequested {
		output.Status = models.StatusPassed
		if len(gradeErrors) > 0 {
			output.Status = models.StatusError
			output.Error = "One or more tasks failed to grade; inspect the saved run diagnostics."
		} else if !allPassed {
			output.Status = models.StatusFailed
		}
	}

	writeSummary := func() error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			return fmt.Errorf("failed to encode results: %w", err)
		}
		return nil
	}
	if !evidenceRequested {
		if err := writeSummary(); err != nil {
			return err
		}
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

		graded := orchestration.RegradeOutcome(outcome, finalOutcomes, effectiveJudgeModel, judgeReasoningEffort)
		graded.RegradeUsage = nil
		if regradeScope != nil {
			graded.RegradeUsage = regradeScope.Snapshot()
		}
		if err := saveOutcome(graded, outputFile); err != nil {
			return errors.Join(errors.Join(gradeErrors...), fmt.Errorf("failed to save graded outcome: %w", err))
		}
	}

	if evidenceRequested {
		gradeErrors = append(gradeErrors, writeSummary())
	}
	return errors.Join(gradeErrors...)
}

func onlyEvidenceGraders(configs []models.GraderConfig, task *models.TestCase) bool {
	if !models.UsesJudgeEvidence(configs, task) {
		return false
	}
	for _, config := range configs {
		parameters, ok := config.Parameters.(models.PromptGraderParameters)
		if !ok || parameters.EvidenceContext == nil {
			return false
		}
	}
	for _, validator := range task.Validators {
		parameters, ok := validator.Parameters.(models.PromptGraderParameters)
		if !ok || parameters.EvidenceContext == nil {
			return false
		}
	}
	return true
}

func loadGradeTasks(spec *models.EvalSpec, specPath, taskID string) ([]*models.TestCase, error) {
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

type evidenceGradeSource struct {
	EvalID   string
	Cached   bool
	Executor graders.Executor
}

func gradeTaskRunsWithJudge(ctx context.Context, spec *models.EvalSpec, tc *models.TestCase, runs []models.RunResult, workspace, judgeModel string, errW io.Writer, verbose bool, source evidenceGradeSource) (models.GradeOutcome, []models.RunResult, error) {
	totalScore := 0.0
	allPassed := true
	graderWeightedTotals := make(map[string]float64)
	graderWeights := make(map[string]float64)
	gradedRuns := make([]models.RunResult, 0, len(runs))

	for i := range runs {
		gradedRun, err := gradeRunWithJudge(ctx, spec, tc, &runs[i], workspace, judgeModel, errW, verbose, source)
		if err != nil {
			if gradedRun != nil {
				gradedRuns = append(gradedRuns, *gradedRun)
			} else {
				gradedRuns = append(gradedRuns, runs[i])
			}
			gradedRuns = append(gradedRuns, runs[i+1:]...)
			return models.GradeOutcome{Passed: false}, gradedRuns, err
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

func gradeRunWithJudge(ctx context.Context, spec *models.EvalSpec, tc *models.TestCase, run *models.RunResult, workspace, judgeModel string, errW io.Writer, verbose bool, source evidenceGradeSource) (*models.RunResult, error) {
	gradedRun := *run
	gradedRun.PromptJudging = slices.Clone(run.PromptJudging)
	usesEvidence := models.UsesJudgeEvidence(spec.Graders, tc)
	fail := func(err error, invalidEvidence bool) (*models.RunResult, error) {
		if usesEvidence && invalidEvidence && len(gradedRun.PromptJudging) == len(run.PromptJudging) {
			configurations := append([]models.GraderConfig(nil), spec.Graders...)
			for _, validator := range tc.Validators {
				configurations = append(configurations, models.GraderConfig{Identifier: validator.Identifier, Parameters: validator.Parameters})
			}
			for _, config := range configurations {
				parameters, ok := config.Parameters.(models.PromptGraderParameters)
				if !ok || parameters.EvidenceContext == nil {
					continue
				}
				observation := models.PromptJudgeObservation{Grader: config.Identifier, Context: "frozen_inline",
					Purpose: "regrade", State: "failed", Inspection: "not_observable", EvidenceContext: *parameters.EvidenceContext,
					Diagnostics: []models.EvidenceDiagnostic{{Category: "prompt_judging", Code: "invalid_evidence",
						Message: "Required frozen evidence is missing or unusable for the requested grading configuration."}}}
				observation.EvidenceContext.Files = slices.Clone(parameters.EvidenceContext.Files)
				observation.EvidenceContext.EvaluatorOnly = slices.Clone(parameters.EvidenceContext.EvaluatorOnly)
				if run.JudgeEvidence != nil {
					observation.BundleSHA256 = run.JudgeEvidence.SHA256
				}
				gradedRun.PromptJudging = append(gradedRun.PromptJudging, observation)
			}
		}
		gradedRun.Validations = map[string]models.GraderResults{}
		gradedRun.Status, gradedRun.ErrorMsg = models.StatusError, err.Error()
		gradedRun.RequirementExplanations = evidence.ExplainRegradedRequirements(spec, tc, &gradedRun)
		return &gradedRun, err
	}
	if run.Evidence != nil {
		updated, err := evidence.Regrade(run.Evidence)
		if err == nil && usesEvidence {
			err = evidence.Bind(updated, source.EvalID, tc.TestID, run.RunNumber, run.Attempts, source.Cached)
		}
		if err != nil {
			if !usesEvidence {
				return nil, fmt.Errorf("original evidence: %w", err)
			}
			gradedRun.Evidence = nil
			if _, writeErr := fmt.Fprintln(errW, "warning: legacy snapshot manifest was withheld because it is invalid or does not match the selected run; record a new execution to restore snapshot provenance"); writeErr != nil {
				return fail(fmt.Errorf("failed to write manifest warning: %w", writeErr), false)
			}
		} else {
			gradedRun.Evidence = updated
		}
	}
	if !usesEvidence && (gradedRun.ErrorMsg != "" || gradedRun.Status == models.StatusError) {
		gradedRun.Validations = map[string]models.GraderResults{}
		gradedRun.Status = models.StatusError
		gradedRun.RequirementExplanations = evidence.ExplainRegradedRequirements(spec, tc, &gradedRun)
		return &gradedRun, nil
	}

	skillInvocations := make([]execution.SkillInvocation, len(run.SkillInvocations))
	for i, si := range run.SkillInvocations {
		skillInvocations[i] = execution.SkillInvocation{Name: si.Name, Path: si.Path}
	}

	gradingCtx := &graders.Context{
		TestCase:           tc,
		Output:             run.FinalOutput,
		Transcript:         run.Transcript,
		Session:            &run.SessionDigest,
		ToolEvents:         run.ToolEvents,
		DurationMS:         run.DurationMs,
		SessionID:          run.SessionDigest.SessionID,
		WorkspaceDir:       workspace,
		SkillInvocations:   skillInvocations,
		Outcome:            make(map[string]any),
		Metadata:           make(map[string]any),
		JudgeModelFallback: spec.Config.ModelID,
	}
	if usesEvidence {
		origin, err := snapshot.CaptureOrigin(snapshot.CaptureInput{EvalID: source.EvalID, Task: tc, Run: run})
		if err != nil {
			return fail(errors.New("evidence judging requires a valid enclosing evaluation/task/run origin"), true)
		}
		gradingCtx.JudgeOrigin, gradingCtx.JudgeEvidence = origin, run.JudgeEvidence
		gradingCtx.JudgingPurpose = "regrade"
		gradingCtx.RecordPromptJudging = func(observation models.PromptJudgeObservation) {
			gradedRun.PromptJudging = append(gradedRun.PromptJudging, observation)
		}
		// Verify the whole run before any model-backed grader is constructed.
		if err := judgeevidence.ValidateSelection(spec.Graders, tc); err != nil {
			return fail(err, true)
		}
		verifyErr := judgeevidence.Verify(run.JudgeEvidence, origin)
		if source.Cached {
			verifyErr = errors.New("evidence judging does not accept cached source runs; record a new execution")
		}
		if verifyErr != nil {
			return fail(verifyErr, true)
		}
		gradedRun.ErrorMsg = ""
		configurations := append([]models.GraderConfig(nil), spec.Graders...)
		for _, validator := range tc.Validators {
			configurations = append(configurations, models.GraderConfig{Identifier: validator.Identifier, Kind: validator.Kind, Parameters: validator.Parameters})
		}
		for _, configuration := range configurations {
			parameters, prompt := configuration.Parameters.(models.PromptGraderParameters)
			if !prompt {
				continue
			}
			if parameters.EvidenceContext == nil {
				return fail(errors.New("evidence regrading cannot be mixed with legacy prompt graders; opt every prompt grader into evidence_context"), true)
			}
			if _, err := judgeevidence.Delivery(run.JudgeEvidence, origin, *parameters.EvidenceContext); err != nil {
				return fail(err, true)
			}
		}
		gradingCtx.EvidenceExecutor = source.Executor
		if gradingCtx.EvidenceExecutor == nil {
			gradingCtx.EvidenceExecutor = execution.NewEvidenceJudgeExecutor()
		}
	}

	if verbose {
		if _, err := fmt.Fprintf(errW, "grading %s: %d transcript events, duration=%dms, session=%s\n",
			tc.TestID, len(run.Transcript), run.DurationMs, run.SessionDigest.SessionID); err != nil {
			return fail(fmt.Errorf("failed to write verbose output: %w", err), false)
		}
	}

	graderResults, err := graders.RunAll(ctx, spec.Graders, tc, gradingCtx, judgeModel, spec.Config.JudgeReasoningEffort, false)
	if err != nil {
		return fail(err, false)
	}

	if verbose {
		for name, result := range graderResults {
			if _, err := fmt.Fprintf(errW, "  grader %s: score=%.2f passed=%v\n", name, result.Score, result.Passed); err != nil {
				return fail(fmt.Errorf("failed to write verbose output: %w", err), false)
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
	gradedRun.RequirementExplanations = evidence.ExplainRegradedRequirements(spec, tc, &gradedRun)

	return &gradedRun, nil
}

func preservedGradeInputs(spec *models.EvalSpec, task *models.TestCase) ([]string, error) {
	for _, check := range []struct {
		name   string
		values []string
	}{
		{"output_contains", task.Expectation.MustInclude},
		{"output_not_contains", task.Expectation.MustExclude},
		{"output_contains_any", task.Expectation.MayInclude},
	} {
		if len(check.values) > 0 {
			return nil, fmt.Errorf("preserved-state grading does not support expected.%s; grade recorded outputs without --evidence-snapshot", check.name)
		}
	}
	var paths []string
	add := func(kind models.GraderKind, params models.GraderParameters) error {
		if kind != models.GraderKindFile {
			return fmt.Errorf("preserved-state grading only supports file graders, not %q; grade recorded outputs without --evidence-snapshot", kind)
		}
		fileParams, ok := params.(models.FileGraderParameters)
		if !ok {
			return fmt.Errorf("preserved-state file grader parameters are missing or incompatible; supply a file grader configuration")
		}
		if len(fileParams.MustNotExist) > 0 {
			return fmt.Errorf("selected-file evidence cannot prove file absence; use the actual workspace")
		}
		paths = append(paths, fileParams.MustExist...)
		for _, pattern := range fileParams.ContentPatterns {
			paths = append(paths, pattern.Path)
		}
		return nil
	}
	for _, grader := range spec.Graders {
		if err := add(grader.Kind, grader.Parameters); err != nil {
			return nil, err
		}
	}
	for _, grader := range task.Validators {
		if err := add(grader.Kind, grader.Parameters); err != nil {
			return nil, err
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no supported explicit file inputs are required; grade the recorded outputs without --evidence-snapshot")
	}
	for _, name := range paths {
		if strings.ContainsAny(name, "*?[") {
			return nil, fmt.Errorf("selected-file evidence cannot certify wildcard inventory; use the actual workspace")
		}
	}
	return paths, nil
}
