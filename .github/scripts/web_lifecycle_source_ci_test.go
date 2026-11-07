package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLifecycleSourceConsumersSelectDashboardValidation(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "workflows", "web-ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	validate := func(source string) bool {
		var workflow siteWorkflow
		if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
			t.Fatal(err)
		}
		steps := releaseSteps(workflow.Jobs["build"])
		var filters map[string][]string
		if err := yaml.Unmarshal([]byte(steps["Detect dashboard changes"].With["filters"]), &filters); err != nil {
			t.Fatal(err)
		}
		for _, patterns := range [][]string{workflow.On["push"].Paths, filters["web"]} {
			for _, name := range []string{"lifecycle.go", "lifecycle_source.go"} {
				if _, err := os.Stat(filepath.Join("..", "..", "internal", "webapi", name)); err != nil {
					t.Fatal(err)
				}
				selected := false
				for _, pattern := range patterns {
					matches, err := filepath.Match(pattern, "internal/webapi/"+name)
					if err != nil {
						t.Fatal(err)
					}
					selected = selected || matches
				}
				if !selected {
					return false
				}
			}
		}
		return true
	}
	if !validate(string(source)) {
		t.Fatal("actual lifecycle API consumers must select dashboard validation")
	}
	for _, line := range []string{
		"      - 'internal/webapi/lifecycle*.go'\n",
		"              - 'internal/webapi/lifecycle*.go'\n",
	} {
		changed := strings.Replace(string(source), line, "", 1)
		if changed == string(source) || validate(changed) {
			t.Fatal("removing either actual lifecycle selector must fail independently")
		}
	}
}
