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
	dockerfile := read("examples/runner/container/Dockerfile")
	copyLine := "COPY examples/runner/unit.json /build/examples/runner/unit.json\n"
	copyIndex := strings.Index(dockerfile, copyLine)
	buildIndex := strings.Index(dockerfile, "RUN npm run build\n")
	if copyIndex < 0 || buildIndex < 0 || copyIndex >= buildIndex {
		t.Fatal("Docker web-builder must copy the runner example before building the dashboard")
	}
	if !strings.Contains(dockerfile, "AS native-reference") || !strings.Contains(dockerfile, "AS native-bundle-test") {
		t.Fatal("runner Dockerfile must distinguish the native reference from its test-only harness")
	}
	ignore := read(".dockerignore")
	admission := "!examples/\nexamples/*\n!examples/runner/\nexamples/runner/*\n!examples/runner/unit.json\n!examples/runner/copilot-unit.json\n"
	if !strings.Contains(ignore, admission) {
		t.Fatal("Docker context must admit the runner example and its parents without including other examples")
	}
	if !strings.Contains(ignore, "!.github/scripts/testdata/native-bundle-probe/harness/main.go") ||
		!strings.Contains(ignore, "!.github/scripts/testdata/native-bundle-probe/positive/main.go") {
		t.Fatal("Docker context must explicitly admit both native bundle probe sources")
	}
	positiveProbe := read(".github/scripts/testdata/native-bundle-probe/positive/main.go")
	if !strings.Contains(positiveProbe, "copilot-sdk/1.0.105/") {
		t.Fatal("native bundle positive control must launch the SDK 1.0.28 bundled CLI 1.0.105")
	}
	if data := read("examples/runner/unit.json"); len(data) == 0 {
		t.Fatal("runner example is empty")
	}

	triggers, err := readTriggers([]byte(read(".github/workflows/docker-ci.yml")))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"push"} {
		if !slices.Contains(triggers[event].Filters["paths"], "examples/runner/unit.json") {
			t.Errorf("%s must select changes to the runner example", event)
		}
		if !slices.Contains(triggers[event].Filters["paths"], "examples/runner/container/**") {
			t.Errorf("%s must select changes to nested runner container inputs", event)
		}
		for _, path := range []string{
			"examples/runner/copilot-unit.json",
			".github/scripts/testdata/runner-runtime/**",
			".github/scripts/testdata/native-bundle-probe/**",
		} {
			if !slices.Contains(triggers[event].Filters["paths"], path) {
				t.Errorf("%s must select native container input %s", event, path)
			}
		}
	}
	workflow := read(".github/workflows/docker-ci.yml")
	if err := checkDockerRunnerBranches([]byte(workflow)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(workflow, "run: node --test examples/runner/container/native-bundle-test.mjs") {
		t.Fatal("Docker CI must execute actual bundled-runtime container controls")
	}
	t.Run("push-main-removal", func(t *testing.T) {
		mutated := strings.Replace(workflow, "push:\n    branches: [main]", "push:\n    branches: []", 1)
		if mutated == workflow {
			t.Fatal("mutation did not remove the push main selector")
		}
		if err := checkDockerRunnerBranches([]byte(mutated)); err == nil {
			t.Fatal("removing main from Docker push discovery must fail")
		}
	})
	t.Run("standalone-pull-request-rejected", func(t *testing.T) {
		mutated := strings.Replace(workflow, "  workflow_call:\n", "  pull_request:\n    branches: [main]\n  workflow_call:\n", 1)
		if mutated == workflow {
			t.Fatal("mutation did not add a standalone pull request trigger")
		}
		if err := checkDockerRunnerBranches([]byte(mutated)); err == nil {
			t.Fatal("pull requests must reach Docker validation only through the go-ci container caller")
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
	if !slices.Equal(triggers["push"].Filters["branches"], []string{"main"}) {
		return fmt.Errorf("Docker pushes must remain limited to main")
	}
	if _, ok := triggers["pull_request"]; ok {
		return fmt.Errorf("Docker pull requests must run only through the go-ci container caller")
	}
	return nil
}
