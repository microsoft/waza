package scripts

import (
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
	}
}
