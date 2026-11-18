package runneradapter

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The adapter must stay a translation layer over the public runner contract.
// These checks fail if execution, storage, scheduling or retry logic creeps in.

var forbiddenDependencies = []string{
	"internal/orchestration", "internal/execution", "internal/runnerunit", "internal/runnersubmission",
	"internal/runnercheckpoint", "internal/executionledger", "internal/executioncheckpoint",
	"internal/artifactmanifest", "internal/webapi", "internal/models", "internal/scoring",
}

var forbiddenImports = []string{"time", "math/rand", "math/rand/v2", "os/signal"}

func TestArchitectureAdapterDependsOnlyOnPublicContracts(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	output, err := exec.Command("go", "list", "-deps", ".").Output()
	require.NoError(t, err)
	for _, dependency := range strings.Fields(string(output)) {
		for _, forbidden := range forbiddenDependencies {
			require.Falsef(t, strings.HasSuffix(dependency, forbidden) || strings.Contains(dependency, forbidden+"/"),
				"adapter must use only the public contract, but depends on %s", dependency)
		}
	}
}

func TestArchitectureAdapterContainsTranslationOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	callSites := 0
	fileSet := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		require.NoError(t, err)
		file, err := parser.ParseFile(fileSet, name, source, 0)
		require.NoError(t, err)
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			for _, forbidden := range forbiddenImports {
				require.NotEqualf(t, forbidden, path, "%s imports %s: timers, randomness and concurrency belong to the external scheduler", name, path)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.GoStmt:
				t.Errorf("%s starts a goroutine; the adapter performs one synchronous call", fileSet.Position(value.Pos()))
			case *ast.ForStmt, *ast.RangeStmt:
				if containsContractCall(value) {
					t.Errorf("%s calls the public contract inside a loop; retries belong to the external scheduler", fileSet.Position(value.Pos()))
				}
			case *ast.CallExpr:
				if isContractCall(value) {
					callSites++
				}
			}
			return true
		})
	}
	require.Equal(t, 1, callSites, "Dispatch must contain exactly one public contract call site")
}

func isContractCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Call" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && ident.Name == "contract"
}

func containsContractCall(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(child ast.Node) bool {
		if call, ok := child.(*ast.CallExpr); ok && isContractCall(call) {
			found = true
		}
		return !found
	})
	return found
}

func TestArchitectureDescriptorRefusesSchedulerFields(t *testing.T) {
	for _, field := range []string{"priority", "queue", "retry", "max_retries", "placement", "node", "pool", "schedule", "deadline", "timeout", "concurrency", "fleet"} {
		translation := Translate(descriptor(t, OperationSubmit, `,"`+field+`":1`))
		require.Equalf(t, "rejected", translation.Status, "descriptor field %q must be rejected", field)
		require.Equal(t, CodeDescriptorUnsupported, translation.Diagnostics[0].Code)
	}
}
