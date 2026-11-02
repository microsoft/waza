// Package runnercompat captures a deliberately bounded local workflow as a
// runner request. It does not construct an engine, discover files or execute work.
package runnercompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"gopkg.in/yaml.v3"
)

// Options names the original local files, not paths to reopen during adaptation.
// TimeoutMS, when omitted, inherits the effective native task/eval timeout.
type Options struct {
	EvalPath  string
	TaskPath  string
	RunKey    string
	Selection runnercontract.Selection
	TimeoutMS *int
}

// AdaptLocal preserves the original UTF-8 documents inside Request.Input.
// Admission is intentionally narrower than ordinary native loaders: unsupported
// declarations are rejected, never removed or coerced into supported defaults.
func AdaptLocal(eval, task []byte, options Options) (*runnercontract.Request, error) {
	request, err := adapt(eval, task, options)
	if err != nil {
		return nil, fmt.Errorf("cannot preserve local semantics in runner 1.0: %w; keep using waza run with the original inputs, or explicitly author a single-task, single-trial, single-attempt inline mock/text suite; see docs/RUNNER_COMPATIBILITY_MIGRATION.md", err)
	}
	return request, nil
}

func adapt(eval, task []byte, options Options) (*runnercontract.Request, error) {
	if len(eval)+len(task) > runnercontract.MaxRequestBytes {
		return nil, fmt.Errorf("captured sources exceed the runner request limit")
	}
	if !utf8.Valid(eval) || !utf8.Valid(task) {
		return nil, fmt.Errorf("captured sources must be valid UTF-8; conversion would change source bytes")
	}
	sourceErr := errors.Join(checkFields(eval, false), checkFields(task, true))
	if sourceErr != nil {
		return nil, sourceErr
	}
	spec, evalErr := models.ParseCapturedEvalSpecOffline(eval, options.EvalPath)
	test, taskErr := models.ParseCapturedTestCaseOffline(task, options.TaskPath, func(string) ([]byte, error) {
		return nil, fmt.Errorf("prompt_file cannot be read during adaptation; inline inputs.prompt explicitly")
	})
	if err := errors.Join(evalErr, taskErr); err != nil {
		return nil, err
	}
	if options.EvalPath == "" || options.TaskPath == "" {
		return nil, fmt.Errorf("exact eval and task filenames are required; discovery and globs are not supported")
	}
	if len(spec.Tasks) != 1 || strings.TrimSpace(spec.Tasks[0]) == "" || strings.ContainsAny(spec.Tasks[0], "*?[") {
		return nil, fmt.Errorf("eval.tasks must name exactly one literal task file; batch/glob/CSV selection is not one local runner unit")
	}
	if filepath.IsAbs(spec.Tasks[0]) {
		return nil, fmt.Errorf("eval.tasks must use an eval-relative filename; absolute task entries are outside the compatibility profile")
	}
	declared := filepath.Join(filepath.Dir(options.EvalPath), spec.Tasks[0])
	declared, err := filepath.Abs(declared)
	if err != nil {
		return nil, fmt.Errorf("resolving declared task filename: %w", err)
	}
	if err := ValidateLiteralTaskPath(declared); err != nil {
		return nil, err
	}
	selected, err := filepath.Abs(options.TaskPath)
	if err != nil {
		return nil, fmt.Errorf("resolving selected task filename: %w", err)
	}
	if declared != selected {
		return nil, fmt.Errorf("selected task filename does not match the sole eval.tasks entry %q; name the declared file", spec.Tasks[0])
	}
	if spec.Config.EngineType != "mock" {
		return nil, fmt.Errorf("config.executor must be mock; native/paid execution has no supported adapter")
	}
	if strings.TrimSpace(spec.Config.ModelID) == "" {
		return nil, fmt.Errorf("config.model must be explicit and nonblank; ambient/default model resolution cannot be captured")
	}
	if !spec.SkillsDisabledForTask(test.SkillPaths) {
		return nil, fmt.Errorf("ambient skill loading is not representable; legacy suites require config.disabled_skills: [\"*\"]")
	}
	if spec.SkillName != "" || len(spec.Config.SkillPaths) != 0 || len(test.SkillPaths) != 0 {
		return nil, fmt.Errorf("skill names/directories are not captured; leave skill empty and use only omitted/null/explicit empty directory lists")
	}
	if spec.Config.InjectSkillBody != nil && *spec.Config.InjectSkillBody {
		return nil, fmt.Errorf("config.inject_skill_body: true is not supported; do not silently drop skill injection")
	}
	if spec.Config.TrialsPerTask != 1 || (spec.Config.MaxAttempts != 0 && spec.Config.MaxAttempts != 1) ||
		options.Selection.Trial != 1 || options.Selection.Attempt != 1 {
		return nil, fmt.Errorf("local adaptation requires one declared trial and attempt (max_attempts omitted/0/1); batch repetitions and retries must remain on waza run")
	}
	if options.Selection.Task != test.TestID {
		return nil, fmt.Errorf("selection.task must equal the captured task id %q", test.TestID)
	}
	if test.Active != nil && !*test.Active {
		return nil, fmt.Errorf("selected task is disabled; waza run skips it, so adapting it to execution would change behavior")
	}
	timeout := spec.Config.TimeoutSec
	if test.TimeoutSec != nil {
		timeout = *test.TimeoutSec
	}
	if timeout < 1 || timeout > 86400 {
		return nil, fmt.Errorf("effective task timeout must be 1..86400 seconds; zero is not an inherited timeout")
	}
	timeoutMS := timeout * 1000
	if options.TimeoutMS != nil && *options.TimeoutMS != timeoutMS {
		return nil, fmt.Errorf("--timeout-ms must equal the effective local timeout %d; omit it to inherit instead of changing deadline semantics", timeoutMS)
	}
	if err := validateChecks(spec, test); err != nil {
		return nil, err
	}
	request := runnercontract.Request{
		SchemaVersion: "1.0", Versions: []string{"1.0"},
		RequiredCapabilities: []string{"exact-unit", "inline.mock-text"}, OptionalCapabilities: []string{},
		RunKey: options.RunKey, Selection: options.Selection,
		Input:     runnercontract.Input{Shard: options.Selection.Shard, Eval: string(eval), Task: string(task)},
		TimeoutMS: timeoutMS, AllowPaidExecution: false,
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding adapted request: %w", err)
	}
	parsed, err := runnercontract.Parse(data)
	if err != nil {
		return nil, err
	}
	admission := runnerunit.Validate(data)
	if admission.Status != "ready" {
		return nil, fmt.Errorf("shared runner admission rejected the captured input: %v", admission.Diagnostics)
	}
	return parsed, nil
}

// ValidateLiteralTaskPath rejects paths that native task loading would interpret
// as a glob, including syntax in eval parent directories. Backslashes are path
// separators on Windows, where filepath.Glob disables escape processing.
func ValidateLiteralTaskPath(path string) error {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving literal task filename: %w", err)
	}
	syntax := "*?["
	if filepath.Separator != '\\' {
		syntax += `\`
	}
	if strings.ContainsAny(resolved, syntax) {
		return fmt.Errorf("complete resolved task path %q must be literal, without glob metacharacters or Unix escapes; keep using waza run with the original inputs", resolved)
	}
	return nil
}

func validateChecks(spec *models.EvalSpec, task *models.TestCase) error {
	seen := map[string]bool{}
	count := 0
	add := func(name string, parameters models.GraderParameters) error {
		text, ok := parameters.(models.TextGraderParameters)
		if !ok {
			return fmt.Errorf("grader %q is not builtin text; retain its original command", name)
		}
		if strings.TrimSpace(name) == "" || strings.HasPrefix(name, "_") || seen[name] {
			return fmt.Errorf("grader %q needs a unique nonblank name without the reserved '_' prefix", name)
		}
		seen[name] = true
		if len(text.Contains)+len(text.NotContains)+len(text.ContainsCS)+len(text.NotContainsCS)+len(text.RegexMatch)+len(text.RegexNotMatch) == 0 {
			return fmt.Errorf("text grader %q must declare a check", name)
		}
		if err := graders.ValidateConfig(name, parameters); err != nil {
			return fmt.Errorf("text grader %q: %w", name, err)
		}
		count++
		return nil
	}
	var diagnostics []error
	for _, grader := range spec.Graders {
		diagnostics = append(diagnostics, add(grader.Identifier, grader.Parameters))
	}
	for _, grader := range task.Validators {
		diagnostics = append(diagnostics, add(grader.Identifier, grader.Parameters))
	}
	for _, list := range [][]string{task.Expectation.MustInclude, task.Expectation.MustExclude, task.Expectation.MayInclude} {
		if len(list) != 0 {
			count++
		}
		for _, assertion := range list {
			if strings.TrimSpace(assertion) == "" {
				diagnostics = append(diagnostics, fmt.Errorf("expected-output assertions must not be blank"))
			}
		}
	}
	if count == 0 {
		diagnostics = append(diagnostics, fmt.Errorf("at least one builtin text grader or expected-output assertion is required"))
	}
	return errors.Join(diagnostics...)
}

// The allowlist rejects unsupported declarations even when null/empty/false.
// Native captured loaders then check wire types, duplicates, versions and values.
func checkFields(data []byte, task bool) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("reading captured source: %w", err)
	}
	if len(doc.Content) != 1 {
		return fmt.Errorf("captured source requires a mapping document")
	}
	fields := map[string]string{
		"schemaVersion": "", "name": "", "description": "", "skill": "", "version": "",
		"scenario": "", "config": "config", "tasks": "", "graders": "graders",
	}
	if task {
		fields = map[string]string{
			"id": "", "name": "", "description": "", "tags": "", "enabled": "",
			"skill_directories": "",
			"timeout_seconds":   "", "inputs": "inputs", "expected": "expected", "graders": "graders",
		}
	}
	return checkMapping(doc.Content[0], fields, "source")
}

func checkMapping(node *yaml.Node, fields map[string]string, prefix string) error {
	if node.Kind != yaml.MappingNode {
		return nil // The native captured loader diagnoses invalid wire types.
	}
	var diagnostics []error
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		child, ok := fields[key]
		if !ok {
			diagnostics = append(diagnostics, fmt.Errorf("unsupported declaration %s.%s (including explicit null/empty/false values)", prefix, key))
			continue
		}
		var nested map[string]string
		switch child {
		case "config":
			nested = map[string]string{
				"executor": "", "model": "", "trials_per_task": "", "max_attempts": "",
				"timeout_seconds": "", "disabled_skills": "", "inject_skill_body": "",
				"skill_directories": "",
			}
		case "inputs":
			nested = map[string]string{"prompt": ""}
		case "expected":
			nested = map[string]string{"output_contains": "", "output_not_contains": "", "output_contains_any": ""}
		case "graders":
			for _, grader := range value.Content {
				diagnostics = append(diagnostics, checkMapping(grader, map[string]string{"name": "", "type": "", "config": ""}, prefix+".graders"))
			}
		}
		if nested != nil {
			diagnostics = append(diagnostics, checkMapping(value, nested, prefix+"."+key))
		}
	}
	return errors.Join(diagnostics...)
}
