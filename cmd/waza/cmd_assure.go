package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/registry"
	"github.com/spf13/cobra"
)

func newAssureCommand() *cobra.Command {
	var referencesPath, reviewPath, acceptedSource, outputPath string
	var calibrate bool
	cmd := &cobra.Command{
		Use:   "assure <eval.yaml>",
		Short: "Challenge scoped graders against evaluator-only reference evidence",
		Long: `Observe native graders against fixed supplied reference labels without a task agent.

Supports complete unredacted preserved files and finite authored output.
Authored output is not historical execution or observed billing. Missing inputs,
unknown capture completeness, absence from a subset, unsupported graders and
unreviewed labels cannot pass assurance. Grader outputs remain separate from
review eligibility and provenance. Agreement describes only this finite corpus.

Executable provenance hashes the observed on-disk path, not the mapped running
image, RAM or authenticity. Replacement can change it or make acquisition fail.

Review declarations require --review and explicit --accept-review-source to
accept that source's current decision; this does not authenticate a human or
discover withheld revocations. Bundled challenge candidates are unreviewed.

Offline by default. Model calibration is not yet available; --calibrate reports
not assessed and makes zero paid calls. Existing grade/run/golden exits and
grader defaults are unchanged. Exit 1 unless strict corpus assessment passes.`,
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) (returnErr error) {
			labels, err := readAssuranceDocument(cmd.Context(), referencesPath)
			if err != nil {
				return err
			}
			references, err := assurance.ParseReferences(labels)
			if err != nil {
				return err
			}
			var review assurance.SuppliedReview
			if reviewPath != "" {
				data, err := readAssuranceDocument(cmd.Context(), reviewPath)
				if err != nil {
					return err
				}
				review, err = assurance.ParseReview(data)
				if err != nil {
					return err
				}
			} else if acceptedSource != "" {
				return errors.New("--accept-review-source requires an explicitly supplied --review document")
			}
			inspection := preflight.Inspect(args[0], preflight.Options{})
			if !inspection.Complete || inspection.Failed(false) {
				return errors.New("assurance prerequisites invalid; run waza preflight for diagnostics")
			}
			spec, err := loadAssuranceEvalSpec(cmd.Context(), args[0])
			if err != nil {
				return errors.New("assurance eval configuration unavailable; run waza preflight for diagnostics")
			}
			tasks, err := loadGradeTasks(spec, args[0], "")
			if err != nil {
				return errors.New("assurance native task declarations unavailable; run waza preflight for diagnostics")
			}
			taskMap := make(map[string]*models.TestCase, len(tasks))
			for _, task := range tasks {
				if taskMap[task.TestID] != nil {
					return errors.New("assurance native task IDs are duplicated")
				}
				taskMap[task.TestID] = task
			}
			source, err := readAssuranceDocument(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			root, err := os.OpenRoot(filepath.Dir(referencesPath))
			if err != nil {
				return errors.New("assurance reference directory unavailable")
			}
			defer func() {
				if err := root.Close(); err != nil {
					returnErr = errors.Join(returnErr, errors.New("closing assurance reference directory failed"))
				}
			}()
			if calibrate {
				if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "Calibration unavailable: no paid calls will be made; the report remains not assessed."); err != nil {
					return fmt.Errorf("writing calibration notice: %w", err)
				}
			}
			report, err := assurance.Verify(cmd.Context(), assurance.VerifyRequest{
				References: references, Review: review,
				Acceptance: assurance.ReviewSourceAcceptance{SourceID: acceptedSource, AcceptCurrentDecision: acceptedSource != ""},
				Now:        time.Now().UTC(), EvalSource: source, Spec: spec, Tasks: taskMap,
				SnapshotRoot: root, Calibrate: calibrate,
			})
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return fmt.Errorf("encoding assurance report: %w", err)
			}
			data = append(data, '\n')
			if outputPath != "" {
				if err := os.WriteFile(outputPath, data, 0o600); err != nil {
					return errors.New("writing assurance report failed")
				}
			}
			if _, err := cmd.OutOrStdout().Write(data); err != nil {
				return fmt.Errorf("writing assurance JSON: %w", err)
			}
			if report.State != assurance.AssessmentPassed {
				return &ExitCodeError{Code: 1, Err: errors.New("assurance did not pass; inspect report states, review and evidence")}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&referencesPath, "references", "", "Strict versioned reference-label JSON; evidence paths are rooted at its directory")
	cmd.Flags().StringVar(&reviewPath, "review", "", "Separate supplied current-review declaration (never inferred from labels)")
	cmd.Flags().StringVar(&acceptedSource, "accept-review-source", "", "Explicitly accept this supplied source's current review decision")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write standalone waza.grader-assurance JSON")
	cmd.Flags().BoolVar(&calibrate, "calibrate", false, "Request calibration; currently unsupported, reports not assessed with zero paid calls")
	if err := cmd.MarkFlagRequired("references"); err != nil {
		panic(err)
	}
	cmd.AddCommand(newAssureCalibrateCommand())
	return cmd
}

func readAssuranceDocument(ctx context.Context, name string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(name))
	if err != nil {
		return nil, errors.New("assurance input is unreadable; supply an existing local document")
	}
	data, readErr := assurance.ReadDocument(ctx, root, filepath.Base(name), assurance.MaxLabelBytes)
	closeErr := root.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("reading assurance input: %w", errors.Join(readErr, closeErr))
	}
	return data, nil
}

func loadAssuranceEvalSpec(ctx context.Context, path string) (*models.EvalSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("loading assurance configuration: %w", err)
	}
	spec, err := models.LoadEvalSpec(path)
	if err != nil {
		return nil, fmt.Errorf("loading assurance configuration: %w", err)
	}
	if len(registry.CollectGraderRefs(spec)) == 0 {
		return spec, nil
	}
	lock, err := models.LoadLockfile(filepath.Join(filepath.Dir(path), models.LockfileName))
	if err != nil {
		return nil, fmt.Errorf("loading assurance grader lock: %w", err)
	}
	resolver, err := registry.NewResolver()
	if err != nil {
		return nil, fmt.Errorf("locating assurance grader cache: %w", err)
	}
	for i, grader := range spec.Graders {
		if grader.Ref == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("resolving assurance grader: %w", err)
		}
		entry, ok := lock.Grader(grader.Ref)
		if !ok {
			return nil, fmt.Errorf("assurance grader %q has no matching lock entry", grader.Ref)
		}
		ref, err := registry.ParseRef(grader.Ref)
		if err != nil {
			return nil, fmt.Errorf("parsing assurance grader reference: %w", err)
		}
		preset, err := resolver.LoadLockedGraderOffline(ctx, ref, entry)
		if err != nil {
			return nil, fmt.Errorf("reading locked assurance grader: %w", err)
		}
		merged, err := registry.MergeGraderConfigOffline(preset, grader)
		if err != nil {
			return nil, fmt.Errorf("merging locked assurance grader: %w", err)
		}
		// Only a verified offline materialization may enter the native observer;
		// the original ref remains bound separately in the unchanged eval bytes.
		merged.Ref = ""
		spec.Graders[i] = merged
	}
	return spec, nil
}
