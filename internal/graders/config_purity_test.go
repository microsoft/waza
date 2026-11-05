package graders

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func TestValidateConfigToolConstraintsPreserveArgumentDeclarations(t *testing.T) {
	for _, field := range []string{"expect", "reject", "allow"} {
		t.Run(field, func(t *testing.T) {
			args := map[string]argmatcher.Matcher{
				"pattern": {Kind: argmatcher.KindRegex, Regex: "^ready$"},
				"schema":  {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"type": "string"}},
			}
			specs := []models.ToolSpecParameters{{Tool: "read", Args: args}}
			var params models.ToolConstraintGraderParameters
			switch field {
			case "expect":
				params.ExpectTools = specs
			case "reject":
				params.RejectTools = specs
			case "allow":
				params.AllowOnly = &specs
			}
			for range 2 {
				require.NoError(t, ValidateConfig("static", params))
				for _, matcher := range args {
					require.False(t, matcher.IsCompiled(), "static validation must not write caches into caller-owned maps")
				}
			}
			var workers sync.WaitGroup
			failures := make(chan error, 8)
			for range 8 {
				workers.Go(func() { failures <- ValidateConfig("parallel", params) })
			}
			workers.Wait()
			close(failures)
			for err := range failures {
				require.NoError(t, err)
			}
			grader, err := NewToolConstraintGrader("runtime", params)
			require.NoError(t, err)
			var normalized []models.ToolSpecParameters
			switch field {
			case "expect":
				normalized = grader.expectTools
			case "reject":
				normalized = grader.rejectTools
			case "allow":
				normalized = *grader.allowOnly
			}
			for _, matcher := range normalized[0].Args {
				require.True(t, matcher.IsCompiled(), "runtime-owned matcher maps must retain compiled caches")
				passed, _ := matcher.Match("ready")
				require.True(t, passed)
			}
			for _, matcher := range args {
				require.False(t, matcher.IsCompiled())
			}
			args["pattern"] = argmatcher.Matcher{Kind: argmatcher.KindRegex, Regex: "^changed$"}
			matcher := normalized[0].Args["pattern"]
			passed, _ := matcher.Match("ready")
			require.True(t, passed, "compiled grader map must not alias the supplied declaration")
		})
	}
}

func TestValidateConfigToolConstraintFailurePreservesDeclarations(t *testing.T) {
	for _, field := range []string{"expect", "reject", "allow"} {
		t.Run(field, func(t *testing.T) {
			args := map[string]argmatcher.Matcher{"pattern": {Kind: argmatcher.KindRegex, Regex: "^ready$"}}
			specs := []models.ToolSpecParameters{
				{Tool: "read", Args: args},
				{Tool: "read", Args: map[string]argmatcher.Matcher{"pattern": {Kind: argmatcher.KindRegex, Regex: "["}}},
			}
			var params models.ToolConstraintGraderParameters
			switch field {
			case "expect":
				params.ExpectTools = specs
			case "reject":
				params.RejectTools = specs
			case "allow":
				params.AllowOnly = &specs
			}
			require.Error(t, ValidateConfig("invalid", params))
			matcher := args["pattern"]
			require.False(t, matcher.IsCompiled(), "a later invalid spec must not leave earlier declarations compiled")
		})
	}
}

func TestValidateConfigProgramUsesPureValidation(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "grader.go", nil, 0)
	require.NoError(t, err)
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok || len(clause.List) != 1 {
			return true
		}
		selector, ok := clause.List[0].(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "ProgramGraderParameters" {
			return true
		}
		for _, statement := range clause.Body {
			if returned, ok := statement.(*ast.ReturnStmt); ok && len(returned.Results) == 1 {
				if call, ok := returned.Results[0].(*ast.CallExpr); ok {
					if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "validateProgramParameters" {
						found = true
					}
				}
			}
		}
		return true
	})
	require.True(t, found, "static program validation must return the pure helper, not construct a runtime grader")
	file, err = parser.ParseFile(token.NewFileSet(), "program_grader.go", nil, 0)
	require.NoError(t, err)
	found = false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "validateProgramParameters" {
			continue
		}
		found = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				selector, ok := call.Fun.(*ast.SelectorExpr)
				require.True(t, ok, "pure helper cannot delegate to runtime or I/O helpers")
				if ok {
					pkg, ok := selector.X.(*ast.Ident)
					require.True(t, ok)
					if ok {
						require.Equal(t, "fmt", pkg.Name)
						require.Equal(t, "Errorf", selector.Sel.Name)
					}
				}
			}
			return true
		})
	}
	require.True(t, found)
	for _, params := range []models.ProgramGraderParameters{
		{Command: "not-an-installed-program"},
		{Command: "echo", Timeout: -1},
		{Command: "echo", Timeout: 17},
	} {
		require.NoError(t, ValidateConfig("static", params))
	}
	require.EqualError(t, ValidateConfig("static", models.ProgramGraderParameters{}), "program grader 'static' must have a 'command'")
}

func TestValidateConfigProgramPreservesRuntimeTimeoutDefaults(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("graders:\n  programTimeout: 17\n"), 0600))
	for _, timeout := range []int{0, -1, 23} {
		params := models.ProgramGraderParameters{Command: "not-an-installed-program", Timeout: timeout}
		require.NoError(t, ValidateConfig("static", params))
		grader, err := NewProgramGrader("runtime", params)
		require.NoError(t, err)
		expected := 17
		if timeout > 0 {
			expected = timeout
		}
		require.Equal(t, time.Duration(expected)*time.Second, grader.timeout)
	}
}

func TestValidateConfigExternalSchemasHaveNoDefaultLoader(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		if err != nil {
			t.Errorf("writing sentinel schema: %v", err)
		}

	}))
	defer server.Close()
	dir := t.TempDir()
	local := filepath.Join(dir, "readable-valid-schema.json")
	require.NoError(t, os.WriteFile(local, []byte(`{"type":"object"}`), 0600))
	fileURI := testutil.FileURL(local)
	for _, target := range []string{server.URL + "/schema", fileURI} {
		t.Run(target, func(t *testing.T) {
			params := models.JSONSchemaGraderParameters{Schema: map[string]any{"$ref": target}}
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			_, err := Create("legacy", params)
			require.NoError(t, err, "runtime constructor keeps its deferred schema behavior")
			require.Zero(t, requests.Load(), "no HTTP schema fetch, including an available valid target")
		})
	}
	require.NoError(t, os.Remove(local))
	params := models.JSONSchemaGraderParameters{Schema: map[string]any{"$ref": fileURI}}
	var loadErr *jsonschema.LoadURLError
	require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
	require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference, "file existence must not affect pure external-reference resolution")
}

func TestValidateConfigArgumentSchemasHaveNoDefaultLoader(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, err := w.Write([]byte(`{"type":"object"}`))
		require.NoError(t, err)
	}))
	defer server.Close()
	local := filepath.Join(t.TempDir(), "schema.json")
	require.NoError(t, os.WriteFile(local, []byte(`{"type":"object"}`), 0600))
	fileURI := testutil.FileURL(local)
	for _, target := range []string{server.URL, fileURI} {
		args := map[string]argmatcher.Matcher{"value": {Kind: argmatcher.KindJSONSchema, JSONSchema: map[string]any{"$ref": target}}}
		specs := []models.ToolSpecParameters{{Tool: "read", Args: args}}
		for _, params := range []models.GraderParameters{
			models.ToolCallsGraderParameters{Expect: []models.ToolExpectation{{Tool: "read", Args: args}}},
			models.ToolConstraintGraderParameters{ExpectTools: specs},
			models.ToolConstraintGraderParameters{RejectTools: specs},
			models.ToolConstraintGraderParameters{AllowOnly: &specs},
		} {
			var loadErr *jsonschema.LoadURLError
			require.ErrorAs(t, ValidateConfig("static", params), &loadErr)
			require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			if target == fileURI {
				_, err := Create("legacy", params)
				require.NoError(t, err, "runtime readable external schemas remain valid")
				require.ErrorAs(t, ValidateConfig("already-compiled", params), &loadErr)
				require.ErrorIs(t, loadErr.Err, schemaloader.ErrExternalReference)
			}
			require.Zero(t, requests.Load(), "HTTP schema requests must remain exactly zero")
		}
	}
}
