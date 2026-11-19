package scripts

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseCLITagGateMutations(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	for _, mutation := range []struct {
		name, old, new string
		admitted       bool
	}{
		{"original", "", "", true},
		{"reordered-flags", `--verify-tag --target "$GITHUB_SHA"`, `--target "$GITHUB_SHA" --verify-tag`, true},
		{"missing-tag-admission", `bash .github/scripts/release_tag_check.sh "$TAG"`, ":", false},
		{"tolerated-tag-admission", `bash .github/scripts/release_tag_check.sh "$TAG"`, `bash .github/scripts/release_tag_check.sh "$TAG" || true`, false},
		{"wrong-guard-tag", `bash .github/scripts/release_tag_check.sh "$TAG"`, `bash .github/scripts/release_tag_check.sh "$GITHUB_SHA"`, false},
		{"missing-verify-tag", "--verify-tag ", "", false},
		{"missing-target", `--target "$GITHUB_SHA"`, "", false},
		{"wrong-target", `--target "$GITHUB_SHA"`, "--target main", false},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			var workflow releaseWorkflow
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			baseline, err := yaml.Marshal(workflow)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateTagRelease(baseline); err != nil {
				t.Fatalf("positive canonical YAML round trip: %v", err)
			}
			job := workflow.Jobs["release-cli"]
			changedRun := ""
			for i := range job.Steps {
				step := &job.Steps[i]
				if step.Name == "Create or Update GitHub Release" {
					changedRun = step.Run
					if mutation.old != "" {
						changedRun = strings.Replace(step.Run, mutation.old, mutation.new, 1)
						if changedRun == step.Run {
							t.Fatal("mutation did not change the selected CLI tag boundary")
						}
					}
					step.Run = changedRun
				}
			}
			if changedRun == "" {
				t.Fatal("canonical CLI publication step missing")
			}
			syntax := exec.CommandContext(t.Context(), "bash", "-n")
			syntax.Stdin = strings.NewReader(changedRun)
			if output, err := syntax.CombinedOutput(); err != nil {
				t.Fatalf("mutation must remain valid Bash: %v\n%s", err, output)
			}
			workflow.Jobs["release-cli"] = job
			changed, err := yaml.Marshal(workflow)
			if err != nil {
				t.Fatal(err)
			}
			var actual releaseWorkflow
			if err := yaml.Unmarshal(changed, &actual); err != nil {
				t.Fatalf("mutation must remain valid YAML: %v", err)
			}
			if releaseSteps(actual.Jobs["release-cli"])["Create or Update GitHub Release"].Run != changedRun {
				t.Fatal("mutation lost the intended CLI Bash source")
			}
			admissionErr := validateTagRelease(changed)
			if admitted := admissionErr == nil; admitted != mutation.admitted {
				t.Fatalf("CLI tag mutation admission = %v, want %v: %v", admitted, mutation.admitted, admissionErr)
			}
		})
	}
}

func TestReleasePublicationMutations(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	for _, stepName := range []string{"Verify selected Extensions Kit contract", "Sync Version Files", "Create exact extension tag exclusively",
		"Require package-only extension release", "Publish Extension Provenance", "Create Release PR", "Build Extension"} {
		for _, field := range []string{"if", "continue-on-error", "run", "shell", "release-flag", "release-flag-absent", "release-flag-job-only"} {
			if strings.HasPrefix(field, "release-flag") && stepName != "Build Extension" {
				continue
			}

			if field == "shell" && stepName != "Sync Version Files" {
				continue
			}
			t.Run(stepName+"/"+field, func(t *testing.T) {
				var wire yaml.Node
				if err := yaml.Unmarshal(source, &wire); err != nil {
					t.Fatal(err)
				}
				baseline, err := yaml.Marshal(&wire)
				if err != nil || validateTagRelease(baseline) != nil {
					t.Fatalf("positive production YAML round trip: %v", err)
				}
				job := releaseMappingValue(t, releaseMappingValue(t, wire.Content[0], "jobs"), "release-extension")
				var target *yaml.Node
				for _, step := range releaseMappingValue(t, job, "steps").Content {
					if releaseMappingValue(t, step, "name").Value == stepName {
						target = step
					}
				}
				if target == nil {
					t.Fatal("production publication boundary missing")
				}
				value, tag := "false", "!!str"
				switch field {
				case "continue-on-error":
					value, tag = "true", "!!bool"
				case "run":
					value = ":"
				case "shell":
					value = "sh"
				}
				found := false
				if field == "release-flag" {
					releaseMappingValue(t, releaseMappingValue(t, target, "env"), "REQUIRE_RELEASE_ADMISSION").Value = "0"
					found = true
				} else if strings.HasPrefix(field, "release-flag-") {
					env := releaseMappingValue(t, target, "env")
					for i := 0; i+1 < len(env.Content); i += 2 {
						if env.Content[i].Value == "REQUIRE_RELEASE_ADMISSION" {
							env.Content = append(env.Content[:i], env.Content[i+2:]...)
							found = true
							break
						}
					}
					if field == "release-flag-job-only" {
						jobEnv := releaseMappingValue(t, job, "env")
						jobEnv.Content = append(jobEnv.Content,
							&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "REQUIRE_RELEASE_ADMISSION"},
							&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "1"})
					}
				}
				for i := 0; i+1 < len(target.Content); i += 2 {
					if target.Content[i].Value == field {
						target.Content[i+1].Value, target.Content[i+1].Tag = value, tag
						found = true
					}
				}
				if !found {
					target.Content = append(target.Content,
						&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: field},
						&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
				}
				changed, err := yaml.Marshal(&wire)
				if err != nil {
					t.Fatal(err)
				}
				var actual releaseWorkflow
				if err := yaml.Unmarshal(changed, &actual); err != nil {
					t.Fatalf("mutation must remain valid YAML: %v", err)
				}
				step := releaseSteps(actual.Jobs["release-extension"])[stepName]
				if strings.HasPrefix(field, "release-flag-") {
					if _, exists := step.Env["REQUIRE_RELEASE_ADMISSION"]; exists ||
						field == "release-flag-job-only" && actual.Jobs["release-extension"].Env["REQUIRE_RELEASE_ADMISSION"] != "1" {
						t.Fatal("missing/ambient-only release flag mutation was not established")
					}
				}
				if string(changed) == string(baseline) || field == "run" && step.Run != ":" ||
					field == "if" && step.If != "false" || field == "continue-on-error" && !step.ContinueOnError ||
					field == "release-flag" && step.Env["REQUIRE_RELEASE_ADMISSION"] != "0" || field == "shell" && step.Shell != "sh" {
					t.Fatal("mutation did not change the intended production field")
				}
				if err := validateTagRelease(changed); err == nil {
					t.Fatal("skipped, tolerated or bypassed publication boundary admitted")
				}
			})
		}
	}
	var workflow releaseWorkflow
	if err := yaml.Unmarshal(source, &workflow); err != nil {
		t.Fatal(err)
	}
	extension := workflow.Jobs["release-extension"]
	provenance, publish := -1, -1
	for i, step := range extension.Steps {
		if step.Name == "Publish Extension Provenance" {
			provenance = i
		}
		if step.Name == "Publish Extension" {
			publish = i
		}
	}
	if provenance <= publish || publish < 0 {
		t.Fatal("current source must publish registry before provenance")
	}
	extension.Steps[provenance], extension.Steps[publish] = extension.Steps[publish], extension.Steps[provenance]
	workflow.Jobs["release-extension"] = extension
	changed, err := yaml.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTagRelease(changed); err == nil {
		t.Fatal("actual sidecar-before-registry ordering admitted")
	}
}

func TestReleaseStableTagGateMutations(t *testing.T) {
	source := readReleaseWorkflow(t, "squad-release.yml")
	for _, mutation := range []string{"missing-checkout", "wrong-checkout-source", "skipped-checkout", "tolerated-checkout",
		"missing-tag-admission", "tolerated-tag-admission", "missing-verify-tag", "wrong-tag-target"} {
		t.Run(mutation, func(t *testing.T) {
			var workflow releaseWorkflow
			if err := yaml.Unmarshal(source, &workflow); err != nil {
				t.Fatal(err)
			}
			baseline, err := yaml.Marshal(workflow)
			if err != nil || validateBranchPublisher(baseline, "squad-release.yml") != nil {
				t.Fatalf("positive stable YAML round trip: %v", err)
			}
			job := workflow.Jobs["release"]
			for i := range job.Steps {
				step := &job.Steps[i]
				if step.Name == "Checkout validated publisher source" {
					switch mutation {
					case "missing-checkout":
						step.Name = "Removed publisher checkout"
					case "wrong-checkout-source":
						step.With["ref"] = "main"
					case "skipped-checkout":
						step.If = "false"
					case "tolerated-checkout":
						step.ContinueOnError = true
					}
				}
				if step.Name == "Create Release" {
					switch mutation {
					case "missing-tag-admission":
						step.Run = strings.ReplaceAll(step.Run, `bash .github/scripts/release_tag_check.sh "$VERSION"`, ":")
					case "tolerated-tag-admission":
						step.Run = strings.ReplaceAll(step.Run, `bash .github/scripts/release_tag_check.sh "$VERSION"`, `bash .github/scripts/release_tag_check.sh "$VERSION" || true`)
					case "missing-verify-tag":
						step.Run = strings.ReplaceAll(step.Run, "--verify-tag ", "")
					case "wrong-tag-target":
						step.Run = strings.ReplaceAll(step.Run, `--target "$GITHUB_SHA"`, "--target main")
					}
				}
			}
			workflow.Jobs["release"] = job
			changed, err := yaml.Marshal(workflow)
			if err != nil || string(changed) == string(baseline) {
				t.Fatalf("valid YAML mutation must actually change selected stable gate: %v", err)
			}
			if err := validateBranchPublisher(changed, "squad-release.yml"); err == nil {
				t.Fatal("unsafe stable tag publication admitted")
			}
		})
	}
}
