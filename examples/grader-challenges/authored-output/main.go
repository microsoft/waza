package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

func main() {
	binary := flag.String("binary", "", "Existing waza binary whose exact bytes are bound")
	directory := flag.String("output-dir", "", "Existing empty evaluator-only output directory")
	flag.Parse()
	if err := generate(*binary, *directory, "examples/grader-challenges/authored-output"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(binary, directory, sourcePath string) error {
	if binary == "" || directory == "" {
		return errors.New("supply --binary and --output-dir")
	}
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("reading selected binary: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		return errors.New("output-dir must be an existing empty directory")
	}
	source, err := os.ReadFile(filepath.Join(sourcePath, "eval.yaml"))
	if err != nil {
		return fmt.Errorf("reading example eval (run from repository root): %w", err)
	}
	taskBytes, err := os.ReadFile(filepath.Join(sourcePath, "task.yaml"))
	if err != nil {
		return fmt.Errorf("reading example task: %w", err)
	}
	spec, err := models.LoadEvalSpec(filepath.Join(sourcePath, "eval.yaml"))
	if err != nil {
		return fmt.Errorf("loading native example eval: %w", err)
	}
	task, err := models.LoadTestCase(filepath.Join(sourcePath, "task.yaml"))
	if err != nil {
		return fmt.Errorf("loading native example task: %w", err)
	}
	if len(spec.Graders) != 1 || len(task.Requirements) != 1 ||
		len(task.Requirements[0].Checks) != 1 || task.Requirements[0].ID != "resulting-state" ||
		task.Requirements[0].Checks[0] != (models.RequirementCheck{Scope: "eval", Grader: spec.Graders[0].Identifier}) {
		return errors.New("example must declare one resulting-state requirement bound to its single eval grader")
	}
	config, err := evidence.JSONDigest(spec)
	if err != nil {
		return fmt.Errorf("binding native config: %w", err)
	}
	taskDigest, err := evidence.JSONDigest(task)
	if err != nil {
		return fmt.Errorf("binding native task: %w", err)
	}
	declaration, err := evidence.JSONDigest(spec.Graders[0])
	if err != nil {
		return fmt.Errorf("binding native grader: %w", err)
	}
	document := assurance.ReferenceDocument{
		Kind: assurance.ReferenceKind, SchemaVersion: assurance.ReferenceVersion,
		ID: "authored-example-candidates", Version: "1",
		EvalSourceSHA256: digest(source), EvalResolvedConfigSHA256: config.SHA256,
		ImplementationExecutableSHA256: new(digest(binaryBytes)),
		Domains:                        []assurance.DomainCriteria{{ID: "repository", MinimumCases: 4, MinimumAgreement: 1}},
	}
	write := func(name string, data []byte) error {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o600); err != nil {
			return fmt.Errorf("writing evaluator-only example artifact: %w", err)
		}
		return nil
	}
	if err := write("eval.yaml", source); err != nil {
		return err
	}
	if err := write("task.yaml", taskBytes); err != nil {
		return err
	}
	for index, candidate := range []struct {
		id             string
		classification assurance.CaseClassification
		output         string
		passed         bool
	}{
		{"good", assurance.CaseGood, "state: ready\n", true},
		{"alternative", assurance.CaseAlternative, "note: another valid explanation\nstate: ready\n", true},
		{"wrong", assurance.CaseCriticalBad, "state: wrong\n", false},
		{"forbidden", assurance.CaseCriticalBad, "state: ready\nforbidden: yes\n", false},
	} {
		input, err := assurance.NewAuthoredOutput(document.ID, candidate.id, task.TestID, index+1, new(candidate.output))
		if err != nil {
			return fmt.Errorf("creating finite authored input: %w", err)
		}
		name := candidate.id + ".json"
		if err := write(name, input.Document()); err != nil {
			return err
		}
		reference, err := evidence.Reference(input.Manifest(), "authored-output")
		if err != nil {
			return fmt.Errorf("creating shared evidence reference: %w", err)
		}
		document.Cases = append(document.Cases, assurance.ReferenceCase{
			ID: candidate.id, ScenarioID: "finite-output", Domain: "repository",
			Classification: candidate.classification, TaskID: task.TestID,
			TaskDeclarationSHA256: taskDigest.SHA256,
			AuthoredInput:         &assurance.AuthoredSource{Path: name, DocumentSHA256: digest(input.Document())},
			ManifestSHA256:        input.Manifest().SHA256,
			Checks: []assurance.ReferenceCheck{{
				RequirementID: "resulting-state", Check: task.Requirements[0].Checks[0],
				GraderDeclarationSHA256: declaration.SHA256, ExpectedPassed: new(candidate.passed),
				Evidence: []models.EvidenceReference{reference},
			}},
		})
	}
	labels, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding example candidate labels: %w", err)
	}
	if _, err := assurance.ParseReferences(labels); err != nil {
		return fmt.Errorf("admitting actual example labels: %w", err)
	}
	return write("labels.json", append(labels, '\n'))
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
