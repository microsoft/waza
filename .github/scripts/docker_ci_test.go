package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerRunnerBuildInputs(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	component := read("web/src/lab/RunnerSelection.tsx")
	if !strings.Contains(component, `import example from "../../../examples/runner/unit.json";`) {
		t.Fatal("runner selection example import changed; update the Docker build-input contract")
	}
	dockerfile := read("Dockerfile")
	copyLine := "COPY examples/runner/unit.json /build/examples/runner/unit.json\n"
	copyIndex := strings.Index(dockerfile, copyLine)
	buildIndex := strings.Index(dockerfile, "RUN npm run build\n")
	if copyIndex < 0 || buildIndex < 0 || copyIndex >= buildIndex {
		t.Fatal("Docker web-builder must copy the runner example before building the dashboard")
	}
	ignore := read(".dockerignore")
	admission := "!examples/\nexamples/*\n!examples/runner/\nexamples/runner/*\n!examples/runner/unit.json\n"
	if !strings.Contains(ignore, admission) {
		t.Fatal("Docker context must admit the runner example and its parents without including other examples")
	}
	if data := read("examples/runner/unit.json"); len(data) == 0 {
		t.Fatal("runner example is empty")
	}

	triggers, err := readTriggers([]byte(read(".github/workflows/docker-ci.yml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"push", "pull_request"} {
		if !slices.Contains(triggers[event]["paths"], "examples/runner/unit.json") {
			t.Errorf("%s must select changes to the runner example", event)
		}
		if !slices.Contains(triggers[event]["paths"], "examples/runner/container/**") {
			t.Errorf("%s must select changes to nested runner container inputs", event)
		}
		for _, path := range []string{"examples/runner/copilot-unit.json", ".github/scripts/testdata/runner-runtime/**"} {
			if !slices.Contains(triggers[event]["paths"], path) {
				t.Errorf("%s must select native container input %s", event, path)
			}
		}
	}
	workflow := read(".github/workflows/docker-ci.yml")
	if err := checkDockerRunnerBranches([]byte(workflow)); err != nil {
		t.Fatal(err)
	}
	t.Run("stacked-parent-removal", func(t *testing.T) {
		mutated := strings.Replace(workflow, "branches: [main, spboyer-native-runner-execution]", "branches: [main]", 1)
		if mutated == workflow {
			t.Fatal("mutation did not remove the stacked parent selector")
		}
		if err := checkDockerRunnerBranches([]byte(mutated)); err == nil {
			t.Fatal("removing the stacked parent must fail Docker discovery validation")
		}
	})
	if !strings.Contains(workflow, "run: node --test examples/runner/container/native-test.mjs") {
		t.Fatal("Docker CI must execute native production-boundary controls")
	}
}

func checkDockerRunnerBranches(source []byte) error {
	triggers, err := readTriggers(source)
	if err != nil {
		return err
	}
	if !slices.Equal(triggers["push"]["branches"], []string{"main"}) {
		return fmt.Errorf("Docker pushes must remain limited to main")
	}
	if !slices.Equal(triggers["pull_request"]["branches"], []string{"main", "spboyer-native-runner-execution"}) {
		return fmt.Errorf("Docker pull requests must select main and the stacked runner parent")
	}
	return nil
}
