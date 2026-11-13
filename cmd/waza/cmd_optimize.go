package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/optimizer"
	"github.com/spf13/cobra"
)

// newReflectEngine builds the engine used by the copilot-reflect adapter.
// Ambient discovery is disabled so proposals only see the supplied candidate
// and optimization feedback, not host instructions, retrieval or memory.
var newReflectEngine = func(model string) execution.AgentEngine {
	return execution.NewCopilotEngineBuilder(model, &execution.CopilotEngineBuilderOptions{
		NewCopilotClient:        newCopilotClientFn,
		DisableAmbientDiscovery: true,
	}).Build()
}

type optimizeRunOptions struct {
	adapter           string
	target            string
	seed              string
	optimizationTags  []string
	qualificationTags []string
	maxIterations     int
	maxEvaluations    int
	timeout           time.Duration
	minImprovement    float64
	outputDir         string
	contextDir        string
	model             string
	reflectModel      string
	fakeScript        string
	adapterCommand    string
	adapterArgs       []string
	adapterEnv        []string
	adapterTimeout    time.Duration
	allowPaidCalls    bool
}

func newOptimizeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "optimize",
		Short: "Run an external optimizer against the evaluation harness (preview)",
		Long: `Run a bounded optimize-evaluate loop through a versioned adapter contract.

Adapters only propose candidate files. Waza materializes each candidate in an
isolated directory, runs it through the normal evaluation runner on the
optimization split, and qualifies the best candidate on a held-out split that
the adapter never sees.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "capabilities",
		Short: "Print the optimizer contract versions and harness capabilities as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(optimizer.Capabilities())
		},
	})
	cmd.AddCommand(newOptimizeRunCommand())
	return cmd
}

func newOptimizeRunCommand() *cobra.Command {
	opts := &optimizeRunOptions{}
	cmd := &cobra.Command{
		Use:           "run <eval.yaml>",
		Short:         "Optimize a seed candidate with an adapter and qualify it on held-out tasks",
		Args:          cobra.ExactArgs(1),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOptimize(cmd, args[0], opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.adapter, "adapter", "fake", "Adapter: fake, command, or copilot-reflect")
	f.StringVar(&opts.target, "target", string(optimizer.TargetSkillDirectory), "Candidate target: skill-directory or context-overlay")
	f.StringVar(&opts.seed, "seed", "", "Directory containing the seed candidate files (required)")
	f.StringSliceVar(&opts.optimizationTags, "optimization-tag", nil, "Task tag pattern for the optimization split (repeatable, required)")
	f.StringSliceVar(&opts.qualificationTags, "qualification-tag", nil, "Task tag pattern for the held-out qualification split (repeatable, required)")
	f.IntVar(&opts.maxIterations, "max-iterations", optimizer.DefaultMaxIterations, "Maximum adapter proposals")
	f.IntVar(&opts.maxEvaluations, "max-evaluations", optimizer.DefaultMaxEvaluations, "Maximum harness evaluations, including 2 reserved for qualification")
	f.DurationVar(&opts.timeout, "timeout", 0, "Deadline for the whole run, including qualification (0 = none)")
	f.Float64Var(&opts.minImprovement, "min-improvement", 0, "Required optimization-split score gain over the seed")
	f.StringVar(&opts.outputDir, "output-dir", "", "New or empty directory for report.json, candidates and evidence (default: waza-optimize-<timestamp>)")
	f.StringVar(&opts.contextDir, "context-dir", "", "Fixture directory (default: <spec-dir>/fixtures)")
	f.StringVar(&opts.model, "model", "", "Override the suite's model for evaluations")
	f.StringVar(&opts.reflectModel, "reflect-model", "", "Model for the copilot-reflect adapter (default: the evaluation model)")
	f.StringVar(&opts.fakeScript, "fake-script", "", "JSON script of proposals for the fake adapter")
	f.StringVar(&opts.adapterCommand, "adapter-command", "", "Executable for the command adapter (run without a shell)")
	f.StringArrayVar(&opts.adapterArgs, "adapter-arg", nil, "Argument for the command adapter (repeatable)")
	f.StringSliceVar(&opts.adapterEnv, "adapter-env", nil, "Environment variable name to pass to the command adapter (repeatable)")
	f.DurationVar(&opts.adapterTimeout, "adapter-timeout", 10*time.Minute, "Timeout for each command adapter call")
	f.BoolVar(&opts.allowPaidCalls, "allow-paid-calls", false, "Consent to paid model calls by the adapter or evaluation executor")
	return cmd
}

func runOptimize(cmd *cobra.Command, specPath string, opts *optimizeRunOptions) (retErr error) {
	target, err := optimizer.ParseTarget(opts.target)
	if err != nil {
		return err
	}
	if opts.seed == "" {
		return errors.New("--seed is required")
	}
	outputDir := opts.outputDir
	if outputDir == "" {
		outputDir = "waza-optimize-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if outputDir, err = filepath.Abs(outputDir); err != nil {
		return fmt.Errorf("resolving --output-dir: %w", err)
	}
	pin, err := optimizer.PinSuite(optimizer.SuiteOptions{
		SpecPath:          specPath,
		ContextDir:        opts.contextDir,
		Target:            target,
		OptimizationTags:  opts.optimizationTags,
		QualificationTags: opts.qualificationTags,
		ExcludeDirs:       []string{outputDir},
		WazaVersion:       version,
		ModelID:           opts.model,
	})
	if err != nil {
		return err
	}
	seed, err := optimizer.LoadSeed(opts.seed, target)
	if err != nil {
		return err
	}

	cfg := optimizer.Config{
		Pin:            pin,
		Target:         target,
		Seed:           seed,
		OutputDir:      outputDir,
		MaxIterations:  opts.maxIterations,
		MaxEvaluations: opts.maxEvaluations,
		Deadline:       opts.timeout,
		MinImprovement: opts.minImprovement,
		AllowPaidCalls: opts.allowPaidCalls,
		ExecutorPaid:   pin.Config.Executor != "mock",
		Evaluator: &optimizer.HarnessEvaluator{
			Pin:           pin,
			Target:        target,
			EngineFactory: newRunEngine,
			OutputDir:     outputDir,
			ExcludeDirs:   []string{outputDir},
			WazaVersion:   version,
		},
	}
	// Registered before any engine cleanup so it runs last: engines built on
	// the process-wide Copilot SDK client leave it running on Shutdown.
	defer func() {
		if err := shutdownSharedClientAfterRun(cmd.Context()); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("shutting down shared copilot client: %w", err))
		}
	}()
	switch opts.adapter {
	case "fake":
		adapter := optimizer.NewFakeAdapter()
		if opts.fakeScript != "" {
			if adapter, err = optimizer.LoadFakeScript(opts.fakeScript); err != nil {
				return err
			}
		}
		cfg.Adapter = adapter
	case "command":
		adapter, err := optimizer.NewCommandAdapter(opts.adapterCommand, opts.adapterArgs, opts.adapterEnv, opts.adapterTimeout)
		if err != nil {
			return err
		}
		cfg.Adapter = adapter
		cfg.AdapterPaid = true
	case "copilot-reflect":
		model := opts.reflectModel
		if model == "" {
			model = pin.Config.Model
		}
		if model == "" || model == "mock" {
			return errors.New("--reflect-model is required when the eval model is unset or mock")
		}
		adapter := optimizer.NewReflectAdapter(newReflectEngine(model), model)
		defer func() {
			if err := adapter.Close(cmd.Context()); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("shutting down copilot-reflect adapter: %w", err))
			}
		}()
		cfg.Adapter = adapter
		cfg.AdapterPaid = true
	default:
		return fmt.Errorf("unknown --adapter %q (use fake, command, or copilot-reflect)", opts.adapter)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := optimizer.Run(ctx, cfg)
	if err != nil {
		return err
	}
	printOptimizeSummary(cmd, report, outputDir)
	if code := report.ExitCode(); code != 0 {
		msg := "optimization " + report.Outcome
		if report.Failure != nil {
			msg += fmt.Sprintf(" (%s): %s", report.Failure.Category, report.Failure.Message)
		}
		return &ExitCodeError{Code: code, Err: errors.New(msg)}
	}
	return nil
}

func printOptimizeSummary(cmd *cobra.Command, r *optimizer.Report, outputDir string) {
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "\nOptimizer outcome: %s\n", r.Outcome)
	if r.Adapter != nil {
		_, _ = fmt.Fprintf(out, "  adapter:     %s %s\n", r.Adapter.Name, r.Adapter.Version)
	}
	_, _ = fmt.Fprintf(out, "  iterations:  %d/%d   evaluations: %d/%d   termination: %s\n",
		r.Budget.IterationsUsed, r.Budget.MaxIterations, r.Budget.EvaluationsUsed, r.Budget.MaxEvaluations, r.Termination)
	if r.BestCandidateID != "" {
		_, _ = fmt.Fprintf(out, "  best:        %s\n", r.BestCandidateID)
	}
	if r.Qualification.Decision != "" {
		_, _ = fmt.Fprintf(out, "  held-out:    %s (%s)\n", r.Qualification.Decision, r.Qualification.DecisionDetails)
	}
	_, _ = fmt.Fprintf(out, "  spend:       %s (no hard spend cap)\n", r.Spend.Total.Status)
	_, _ = fmt.Fprintf(out, "  report:      %s\n", filepath.Join(outputDir, "report.json"))
}
