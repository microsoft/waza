package preflight

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func inspectGraders(r *Report, configs []models.GraderConfig, source, task, fixtures string) {
	names := map[string]bool{}
	for _, g := range configs {
		if g.Identifier == "" || names[g.Identifier] {
			r.add("grader.name", Invalid, source, task, "", "Grader name is empty or duplicated within its scope.", "Assign unique nonempty grader names within each eval, task, or checkpoint scope.")
		}
		names[g.Identifier] = true
		if g.Ref != "" && g.Kind == "" {
			// Lock diagnostics already explain why the preset could not be expanded.
			continue
		}
		if err := g.Validate(); err != nil {
			r.add("grader.configuration", Invalid, source, task, "", "Grader configuration is structurally invalid.", "Check the existing grader's required config fields.")
			continue
		}
		if err := graders.ValidateConfig(g.Identifier, g.Parameters); err != nil {
			r.add("grader.configuration", schemaState(err), source, task, "", "Static grader configuration could not be verified.", "Check grader regexes, matching modes, bounds, and self-contained schemas.")
		}
		switch p := g.Parameters.(type) {
		case models.InlineScriptGraderParameters:
			r.add("grader.subprocess", Unresolved, source, task, "", "Inline-script interpreter and assertions were not executed.", "Verify the interpreter and assertion semantics explicitly outside preflight.")
		case models.ProgramGraderParameters:
			r.Dependencies = append(r.Dependencies, Dependency{Kind: "grader", Name: g.Identifier, Mode: "subprocess", State: Unresolved, TaskID: task})
			r.add("grader.subprocess", Unresolved, source, task, "", "Program grader was not run; executable availability and effects are unresolved.", "Review the executable and run it explicitly outside preflight.")
		case models.PromptGraderParameters:
			if p.Rubric != "" {
				if _, err := graders.ResolveRubric(p.Rubric); err != nil {
					r.add("grader.rubric", Invalid, source, task, "", "Grader rubric cannot be resolved or parsed locally.", "Use an existing built-in rubric or a valid local rubric file (relative paths resolve from cwd).")
				}
			}
			r.Dependencies = append(r.Dependencies, Dependency{Kind: "grader", Name: g.Identifier, Mode: "model", State: Unresolved, TaskID: task})
			r.add("grader.model", Unresolved, source, task, "", "Model-backed grader was not executed.", "Verify judge availability and inspect the eventual grading evidence.")
		case models.TriggerHeuristicGraderParameters:
			if err := graders.ValidateTriggerSource(p); err != nil {
				r.add("grader.trigger_source", Invalid, source, task, "", "Trigger grader skill data is unreadable, malformed, or has no usable trigger keywords.", "Use a readable local SKILL.md file or skill directory at the runtime path (relative paths resolve from cwd).")
			}
		case models.JSONSchemaGraderParameters:
			doc := p.Schema
			location := filepath.Join(fixtures, "preflight-inline-schema.json")
			if doc == nil {
				location = p.SchemaFile
				data, err := os.ReadFile(location)
				if err != nil || json.Unmarshal(data, &doc) != nil {
					r.add("grader.schema_file", Invalid, source, task, "", "Grader schema file is unreadable or is not a JSON object.", "Use a readable JSON schema at the runtime-resolved path (relative schema_file resolves from cwd).")
					continue
				}
			}
			if err := compileOfflineSchema(doc, location); err != nil {
				r.add("grader.schema", schemaState(err), source, task, "", "JSON schema cannot be compiled from self-contained local evidence.", "Use valid self-contained schemas; external refs are never fetched by preflight.")
			}
		case models.DiffGraderParameters:
			for _, expected := range p.ExpectedFiles {
				if !safeWorkspacePath(expected.Path) {
					r.add("grader.diff_path", Invalid, source, task, "", "Diff expected file is not a bounded workspace-relative path.", "Use workspace-relative expected file paths.")
				}
				if expected.Snapshot != "" {
					path, err := graders.ResolveSnapshotPath(p.ContextDir, expected.Snapshot)
					if err != nil {
						r.add("grader.snapshot", Invalid, source, task, "", "Snapshot path cannot be resolved within its runtime context.", "Use a readable snapshot within the configured grader context, including resolved symlinks.")
					} else {
						checkFile(r, path, source, task, "grader.snapshot")
					}
				}
			}
			if p.UpdateSnapshots {
				r.add("grader.snapshot_update", Unresolved, source, task, "", "Snapshot update policy is declared; preflight does not write snapshots.", "Review intentional snapshot changes during execution.")
			}
		case models.FileGraderParameters:
			paths := append(append([]string{}, p.MustExist...), p.MustNotExist...)
			for _, pattern := range p.ContentPatterns {
				paths = append(paths, pattern.Path)
			}
			for _, path := range paths {
				if !safeWorkspacePath(path) {
					r.add("grader.file_path", Invalid, source, task, "", "File check path is not workspace-relative.", "Use bounded workspace-relative file paths.")
				}
			}
		}
	}
}

func compileOfflineSchema(doc map[string]any, location string) error {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	if err := compiler.AddResource(location, doc); err != nil {
		return fmt.Errorf("adding local schema: %w", err)
	}
	if _, err := compiler.Compile(location); err != nil {
		return fmt.Errorf("compiling local schema: %w", err)
	}
	return nil
}
