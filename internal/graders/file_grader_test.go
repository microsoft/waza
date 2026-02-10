package graders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/spboyer/waza/internal/models"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestFileGrader_Basic(t *testing.T) {
	g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"file.txt"}})
	require.NoError(t, err)

	require.Equal(t, TypeFile, g.Type())
	require.Equal(t, "test", g.Name())
}

func TestFileGrader_Grade(t *testing.T) {
	t.Run("file must_exist passes when file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"test.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "All file checks passed", results.Feedback)
	})

	t.Run("file must_exist fails when file missing", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"missing.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "File must exist but not found: missing.txt")
	})

	t.Run("file must_not_exist passes when file absent", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustNotExist: []string{"should-not-exist.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("file must_not_exist fails when file present", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "forbidden.txt"), []byte("oops"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustNotExist: []string{"forbidden.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "File must not exist but found: forbidden.txt")
	})

	t.Run("content patterns must_match passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "code.go"), []byte("func main() {\n\tfmt.Println(\"hello\")\n}"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "code.go", MustMatch: []string{`func main`, `fmt\.Println`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("content patterns must_match fails when pattern missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "code.go"), []byte("package main"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "code.go", MustMatch: []string{`func main`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "File code.go missing expected pattern: func main")
	})

	t.Run("content patterns must_not_match passes when pattern absent", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "safe.go"), []byte("func main() {}"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "safe.go", MustNotMatch: []string{`panic`, `os\.Exit`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("content patterns must_not_match fails when forbidden pattern found", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "bad.go"), []byte("func main() { panic(\"boom\") }"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "bad.go", MustNotMatch: []string{`panic`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "File bad.go contains forbidden pattern: panic")
	})

	t.Run("content patterns file not found", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "missing.go", MustMatch: []string{`anything`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "File not found for content check: missing.go")
	})

	t.Run("content patterns must_not_match on missing file reports unverifiable", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "missing.go", MustNotMatch: []string{`forbidden_pattern`, `another_bad`}},
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Contains(t, results.Feedback, "File not found for content check: missing.go")
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "could not verify absence of pattern (file not found): forbidden_pattern")
		require.Contains(t, results.Feedback, "could not verify absence of pattern (file not found): another_bad")
	})

	t.Run("combined checks partial failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "exists.txt"), []byte("content"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "forbidden.txt"), []byte("bad"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"exists.txt", "missing.txt"}, MustNotExist: []string{"forbidden.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		// 1 of 3 passed (exists.txt), 2 failed (missing.txt, forbidden.txt)
		require.InDelta(t, 1.0/3.0, results.Score, 0.01)
		require.Contains(t, results.Feedback, "File must exist but not found: missing.txt")
		require.Contains(t, results.Feedback, "File must not exist but found: forbidden.txt")
	})

	t.Run("invalid regex reports failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("content"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "test.txt", MustMatch: []string{`[invalid`}}, //nolint:staticcheck // intentionally invalid regex for testing
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)
		require.Contains(t, results.Feedback, "Invalid 'must_match' regex pattern \"[invalid\"")
	})

	t.Run("invalid regex in must_not_match reports failure", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("content"), 0644))

		badRegexPattern := `[invalid` + `_regex` //nolint:staticcheck // intentionally invalid regex for testing

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "test.txt", MustNotMatch: []string{badRegexPattern}}, // invalid (on purpose): no closing ]
		}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.5, results.Score)

		require.Contains(t, results.Feedback, "Invalid 'must_not_match' regex pattern \"[invalid_regex\"")

		// we should also see the actual regex compilation error in there. I'll create an invalid regex
		// so I know what the error message should be.
		_, err = regexp.Compile(badRegexPattern)
		require.Error(t, err)
		require.Contains(t, results.Feedback, err.Error())
	})

	t.Run("no workspace directory fails gracefully", func(t *testing.T) {
		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"file.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: "",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "No workspace directory available")
	})

	t.Run("no checks returns error from constructor", func(t *testing.T) {
		_, err := NewFileGrader(FileGraderArgs{Name: "test"})
		require.Error(t, err)
		require.EqualError(t, err, fmt.Sprintf(errFileGraderNoChecks, "test"))
	})

	t.Run("nested file paths work", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "src", "pkg"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "src", "pkg", "main.go"), []byte("package main"), 0644))

		g, err := NewFileGrader(FileGraderArgs{
			Name:      "test",
			MustExist: []string{"src/pkg/main.go"},
			ContentPatterns: []FileContentPattern{
				{Path: "src/pkg/main.go", MustMatch: []string{`package main`}},
			},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("result details contains expected fields", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "detail-test", MustExist: []string{"test.txt"}, MustNotExist: []string{"bad.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.Equal(t, "detail-test", results.Name)
		require.Equal(t, "file", results.Type)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, []string{"test.txt"}, results.Details["must_exist"])
		require.Equal(t, []string{"bad.txt"}, results.Details["must_not_exist"])
		require.Equal(t, tmpDir, results.Details["workspace_dir"])
	})

	t.Run("duration is recorded", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "exists.txt"), []byte("hi"), 0644))

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"exists.txt"}})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})

	t.Run("path traversal in must_exist is rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"../../etc/passwd"}})
		require.NoError(t, err)

		_, err = g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outside workspace")
	})

	t.Run("path traversal in must_not_exist is rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustNotExist: []string{"../secret.key"}})
		require.NoError(t, err)

		_, err = g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outside workspace")
	})

	t.Run("path traversal in content_patterns is rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", ContentPatterns: []FileContentPattern{
			{Path: "../../../etc/shadow", MustMatch: []string{`root`}},
		}})
		require.NoError(t, err)

		_, err = g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "outside workspace")
	})

	t.Run("absolute path outside workspace is rejected", func(t *testing.T) {
		tmpDir := t.TempDir()

		g, err := NewFileGrader(FileGraderArgs{Name: "test", MustExist: []string{"/etc/passwd"}})
		require.NoError(t, err)

		_, err = g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "absolute and not relative to workspace")
	})
}

func TestFileGrader_ViaCreate(t *testing.T) {
	t.Run("Create with TypeFile works", func(t *testing.T) {
		tmpDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "hello.txt"), []byte("hello world"), 0644))

		// Parse config from YAML to mirror how eval specs define file graders.
		yamlConfig := `
must_exist:
  - "hello.txt"
must_not_exist:
  - "bad.txt"
content_patterns:
  - path: "hello.txt"
    must_match:
      - "hello"
`
		var config map[string]any
		require.NoError(t, yaml.Unmarshal([]byte(yamlConfig), &config))

		g, err := Create(TypeFile, "from-create", config)
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, TypeFile, g.Type())

		results, err := g.Grade(context.Background(), &Context{
			WorkspaceDir: tmpDir,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure FileGrader satisfies the Grader interface at compile time.
var _ Grader = (*fileGrader)(nil)
var _ *models.GraderResults // ensure import is used
