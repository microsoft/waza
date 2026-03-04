package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	waza "github.com/microsoft/waza"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/scaffold"
	suggestpkg "github.com/microsoft/waza/internal/suggest"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var newSuggestEngine = func(modelID string) execution.AgentEngine {
	return execution.NewCopilotEngineBuilder(modelID, nil).Build()
}

type suggestFlags struct {
	model     string
	dryRun    bool
	apply     bool
	outputDir string
	format    string
}

func newSuggestCommand() *cobra.Command {
	_, defaultModel := scaffold.ReadProjectDefaults()
	flags := &suggestFlags{
		model:  defaultModel,
		dryRun: true,
		format: "yaml",
	}

	cmd := &cobra.Command{
		Use:   "suggest <skill-path>",
		Short: "Suggest eval files for a skill (experimental)",
		Long: `Analyze a SKILL.md with an LLM and generate suggested eval artifacts.

This command is experimental. Because an LLM generates suggestions, they should be
reviewed by a human before applying. By default, suggestions are printed to stdout
(--dry-run). Use --apply to write suggested eval.yaml, tasks, and fixtures to disk.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuggestCommand(cmd, args[0], flags)
		},
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&flags.model, "model", flags.model, "Model to use for suggestions")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", true, "Print suggestions to stdout without writing files")
	cmd.Flags().BoolVar(&flags.apply, "apply", false, "Write suggested files to disk")
	cmd.Flags().StringVar(&flags.outputDir, "output-dir", "", "Directory for output (default: <skill-path>/evals)")
	cmd.Flags().StringVar(&flags.format, "format", "yaml", "Output format: yaml|json")

	return cmd
}

func runSuggestCommand(cmd *cobra.Command, skillPath string, flags *suggestFlags) error {
	if flags.format != "yaml" && flags.format != "json" {
		return fmt.Errorf("invalid format %q: must be yaml or json", flags.format)
	}
	if flags.apply {
		flags.dryRun = false
	}
	if !flags.apply && !flags.dryRun {
		return errors.New("either --dry-run or --apply must be enabled")
	}

	outputDir := flags.outputDir
	if outputDir == "" {
		outputDir = defaultSuggestOutputDir(skillPath)
	}

	engine := newSuggestEngine(flags.model)

	if err := engine.Initialize(cmd.Context()); err != nil {
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		_ = engine.Shutdown(shutdownCtx)
	}()

	suggestion, err := suggestpkg.Generate(cmd.Context(), engine, suggestpkg.Options{
		SkillPath:  skillPath,
		GraderDocs: waza.GraderDocsFS,
	})
	if err != nil {
		return err
	}

	if flags.dryRun {
		data, err := marshalSuggestOutput(flags.format, suggestion)
		if err != nil {
			return err
		}
		_, _ = cmd.OutOrStdout().Write(data)
		if len(data) == 0 || data[len(data)-1] != '\n' {
			_, _ = cmd.OutOrStdout().Write([]byte("\n"))
		}
		return nil
	}

	written, err := suggestion.WriteToDir(outputDir)
	if err != nil {
		return err
	}

	applyOutput := struct {
		OutputDir string   `yaml:"output_dir" json:"output_dir"`
		Files     []string `yaml:"files" json:"files"`
	}{
		OutputDir: outputDir,
		Files:     written,
	}

	data, err := marshalApplyOutput(flags.format, applyOutput)
	if err != nil {
		return err
	}
	_, _ = cmd.OutOrStdout().Write(data)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		_, _ = cmd.OutOrStdout().Write([]byte("\n"))
	}
	return nil
}

func defaultSuggestOutputDir(skillPath string) string {
	candidate := skillPath
	if strings.EqualFold(filepath.Base(skillPath), "SKILL.md") {
		candidate = filepath.Dir(skillPath)
	}
	return filepath.Join(candidate, "evals")
}

func marshalSuggestOutput(format string, suggestion *suggestpkg.Suggestion) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(suggestion, "", "  ")
	case "yaml":
		return yaml.Marshal(suggestion)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}

func marshalApplyOutput(format string, value any) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(value, "", "  ")
	case "yaml":
		return yaml.Marshal(value)
	default:
		return nil, fmt.Errorf("unsupported format: %s", format)
	}
}
