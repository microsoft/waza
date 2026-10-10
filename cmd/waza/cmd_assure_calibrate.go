package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/spf13/cobra"
)

type assureCalibrateCommandOptions struct {
	Calibrate     func(context.Context, assurance.CalibrateRequest) (*assurance.Report, error)
	EngineFactory func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error)
	Now           func() time.Time
	SignalContext func(context.Context) (context.Context, context.CancelFunc)
	CloseRoot     func(*os.Root) error
}

func newAssureCalibrateCommand() *cobra.Command {
	return newAssureCalibrateCommandWithOptions(nil)
}

func newAssureCalibrateCommandWithOptions(options *assureCalibrateCommandOptions) *cobra.Command {
	opts := assureCalibrateCommandOptions{
		Calibrate: assurance.Calibrate,
		EngineFactory: func(model string, observer func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
			return execution.NewCopilotEngineBuilder(model, &execution.CopilotEngineBuilderOptions{
				NewCopilotClient:   execution.NewOwnedCopilotClient,
				DiagnosticObserver: observer,
			}).Build(), nil
		},
		Now: time.Now,
		SignalContext: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		},
		CloseRoot: func(root *os.Root) error { return root.Close() },
	}
	if options != nil {
		if options.Calibrate != nil {
			opts.Calibrate = options.Calibrate
		}
		if options.EngineFactory != nil {
			opts.EngineFactory = options.EngineFactory
		}
		if options.Now != nil {
			opts.Now = options.Now
		}
		if options.SignalContext != nil {
			opts.SignalContext = options.SignalContext
		}
		if options.CloseRoot != nil {
			opts.CloseRoot = options.CloseRoot
		}
	}
	var referencesPath, reviewPath, acceptedSource, rubricPath, outputPath string
	var acceptPaid bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "calibrate <eval.yaml>",
		Short: "Explicitly calibrate independent judges against reviewed authored output",
		Long: `Calibrate independent native judges against a finite supplied reviewed corpus.

Requires separate references, current review-source acceptance and an explicit
rubric directory. Review-source acceptance does not authenticate a human or
discover withheld revocations. No task agent, skills or reused sessions.
Paid calls require --accept-paid-calls for this invocation, even without a TTY.
The reviewed execution ceiling bounds independent jobs, not follow-up calls,
retries, credits or money. Unknown finalized accounting is not zero.
--timeout is a cooperative assessment deadline, not a spending budget.
Exit 0: clean pass; 1: ordinary nonpass/refusal; 2: invalid or operational error.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			fail := func(err error) error { return &ExitCodeError{Code: 2, Err: err} }
			for _, value := range []string{referencesPath, reviewPath, acceptedSource, rubricPath} {
				if strings.TrimSpace(value) == "" {
					return fail(errors.New("calibration requires nonempty --references, --review, --accept-review-source and --rubric-root"))
				}
			}
			if timeout <= 0 {
				return fail(errors.New("calibration --timeout must be positive"))
			}
			if err := calibrationProviderEnvironment(); err != nil {
				return fail(err)
			}
			signalCtx, stop := opts.SignalContext(cmd.Context())
			defer stop()
			ctx, cancel := context.WithTimeout(signalCtx, timeout)
			defer cancel()
			if ctx.Err() != nil {
				return fail(errors.New("calibration assessment interrupted"))
			}
			labels, err := readAssuranceDocument(ctx, referencesPath)
			if err != nil {
				return fail(errors.New("calibration references unavailable"))
			}
			references, err := assurance.ParseReferences(labels)
			if err != nil {
				return fail(errors.New("calibration references invalid"))
			}
			reviewData, err := readAssuranceDocument(ctx, reviewPath)
			if err != nil {
				return fail(errors.New("calibration review unavailable"))
			}
			review, err := assurance.ParseReview(reviewData)
			if err != nil {
				return fail(errors.New("calibration review invalid"))
			}
			inspection := preflight.Inspect(args[0], preflight.Options{})
			if !inspection.Complete || inspection.Failed(false) {
				return fail(errors.New("calibration prerequisites invalid; run waza preflight for diagnostics"))
			}
			spec, err := models.LoadEvalSpec(args[0])
			if err != nil {
				return fail(errors.New("calibration eval configuration unavailable"))
			}
			tasks, err := loadGradeTasks(spec, args[0], "")
			if err != nil {
				return fail(errors.New("calibration native task declarations unavailable"))
			}
			taskMap := make(map[string]*models.TestCase, len(tasks))
			for _, task := range tasks {
				if taskMap[task.TestID] != nil {
					return fail(errors.New("calibration native task IDs are duplicated"))
				}
				taskMap[task.TestID] = task
			}
			source, err := readAssuranceDocument(ctx, args[0])
			if err != nil {
				return fail(errors.New("calibration eval source unavailable"))
			}
			var operational error
			// Roots are borrowed by Calibrate; close them before selecting the exit.
			defer func() {
				if ctx.Err() != nil {
					operational = errors.Join(operational, errors.New("calibration assessment interrupted"))
				}
				if operational != nil {
					returnErr = fail(errors.Join(returnErr, operational))
				}
			}()
			evidenceRoot, err := os.OpenRoot(filepath.Dir(referencesPath))
			if err != nil {
				return fail(errors.New("calibration reference directory unavailable"))
			}
			defer func() {
				if opts.CloseRoot(evidenceRoot) != nil {
					operational = errors.Join(operational, errors.New("closing calibration reference directory failed"))
				}
			}()
			rubricRoot, err := os.OpenRoot(rubricPath)
			if err != nil {
				return fail(errors.New("calibration rubric directory unavailable"))
			}
			defer func() {
				if opts.CloseRoot(rubricRoot) != nil {
					operational = errors.Join(operational, errors.New("closing calibration rubric directory failed"))
				}
			}()
			var noticeIOError error
			report, producerErr := opts.Calibrate(ctx, assurance.CalibrateRequest{
				VerifyRequest: assurance.VerifyRequest{
					References: references, Review: review,
					Acceptance: assurance.ReviewSourceAcceptance{SourceID: acceptedSource, AcceptCurrentDecision: true},
					Now:        opts.Now().UTC(), EvalSource: source, Spec: spec, Tasks: taskMap,
					SnapshotRoot: evidenceRoot, Calibrate: true,
				},
				RubricRoot: rubricRoot,
				EngineFactory: func(model string, observer func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
					if err := calibrationProviderEnvironment(); err != nil {
						return nil, err
					}
					return opts.EngineFactory(model, observer)
				},
				PaidCallNotice: func(plan assurance.CalibrationPlan, admitted int) error {
					notice := fmt.Sprintf("Calibration paid-call notice: reviewed model=%q protocol=%q; admitted unique executions N=%d; declared maximum M=%d; acknowledgment=%t.\nN<=M bounds independent jobs only, not provider follow-up calls, retries, credits or money. Unavailable finalized accounting is not zero.\n", plan.Model, plan.Protocol, admitted, plan.MaxJudgeExecutions, acceptPaid)
					if err := writeCalibrationBytes(cmd.ErrOrStderr(), []byte(notice)); err != nil {
						noticeIOError = errors.New("writing calibration paid-call notice failed")
						return noticeIOError
					}
					if ctx.Err() != nil {
						return errors.New("calibration assessment interrupted")
					}
					if !acceptPaid {
						return errors.New("calibration paid calls not acknowledged")
					}
					return nil
				},
			})
			if producerErr != nil {
				operational = errors.Join(operational, errors.New("calibration producer failed; inspect any partial report"))
			}
			operational = errors.Join(operational, noticeIOError)
			if report == nil {
				operational = errors.Join(operational, errors.New("calibration report unavailable"))
				return nil
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				operational = errors.Join(operational, errors.New("encoding calibration report failed"))
				return nil
			}
			data = append(data, '\n')
			if outputPath != "" {
				if err := writeCalibrationReport(outputPath, data); err != nil {
					operational = errors.Join(operational, err)
				}
			}
			if err := writeCalibrationBytes(cmd.OutOrStdout(), data); err != nil {
				operational = errors.Join(operational, errors.New("writing calibration JSON failed"))
			}
			switch report.State {
			case assurance.AssessmentPassed:
				return nil
			case assurance.AssessmentFailed, assurance.AssessmentNotAssessed, assurance.AssessmentInsufficient:
				return &ExitCodeError{Code: 1, Err: errors.New("calibration did not pass; inspect the report")}
			default:
				operational = errors.Join(operational, errors.New("calibration report is invalid or operationally failed"))
				return nil
			}
		},
	}
	cmd.Flags().StringVar(&referencesPath, "references", "", "Strict supplied reference-label JSON")
	cmd.Flags().StringVar(&reviewPath, "review", "", "Separate supplied current-review declaration")
	cmd.Flags().StringVar(&acceptedSource, "accept-review-source", "", "Accept this supplied source's current review decision")
	cmd.Flags().StringVar(&rubricPath, "rubric-root", "", "Explicit directory confining original local rubric reads")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Atomically write a private standalone calibration report")
	cmd.Flags().BoolVar(&acceptPaid, "accept-paid-calls", false, "Acknowledge paid calls for this admitted invocation only")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "Positive cooperative assessment timeout, not a spending budget")
	for _, name := range []string{"references", "review", "accept-review-source", "rubric-root"} {
		if err := cmd.MarkFlagRequired(name); err != nil {
			panic(err)
		}
	}
	return cmd
}

func calibrationProviderEnvironment() error {
	for _, name := range []string{"COPILOT_BASE_URL", "COPILOT_PROVIDER_BASE_URL"} {
		if os.Getenv(name) != "" {
			return errors.New("calibration rejects ambient provider redirects; unset COPILOT_BASE_URL and COPILOT_PROVIDER_BASE_URL")
		}
	}
	return nil
}

func writeCalibrationBytes(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}

func writeCalibrationReport(path string, data []byte) (returnErr error) {
	// Exclusive private sibling staging preserves the old report on failure.
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return errors.New("creating calibration report staging name failed")
	}
	staging := filepath.Join(filepath.Dir(path), ".waza-calibration-"+hex.EncodeToString(suffix[:]))
	file, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("creating calibration report staging file failed")
	}
	defer func() {
		if err := os.Remove(staging); err != nil && !os.IsNotExist(err) {
			returnErr = errors.Join(returnErr, errors.New("removing calibration report staging file failed"))
		}
	}()
	writeErr := writeCalibrationBytes(file, data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("writing calibration report staging file failed")
	}
	if err := os.Rename(staging, path); err != nil {
		return errors.New("replacing calibration report failed")
	}
	return nil
}
