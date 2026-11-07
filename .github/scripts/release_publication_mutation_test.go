package scripts

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleasePublicationMutations(t *testing.T) {
	source := readReleaseWorkflow(t, "release.yml")
	for _, stepName := range []string{"Verify selected Extensions Kit contract", "Sync Version Files", "Create exact extension tag exclusively",
		"Require package-only extension release", "Publish Extension Provenance", "Create Release PR", "Build Extension"} {
		for _, field := range []string{"if", "continue-on-error", "run", "release-flag", "release-flag-absent", "release-flag-job-only"} {
			if strings.HasPrefix(field, "release-flag") && stepName != "Build Extension" {
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
					field == "release-flag" && step.Env["REQUIRE_RELEASE_ADMISSION"] != "0" {
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
