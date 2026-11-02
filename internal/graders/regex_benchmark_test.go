package graders

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func BenchmarkTextGraderRegexes(b *testing.B) {
	patterns := make([]string, 16)
	for i := range patterns {
		patterns[i] = fmt.Sprintf(`(?m)^section-%02d: [a-z]+$`, i)
	}
	grader, err := NewTextGrader("text", models.TextGraderParameters{
		RegexMatch: patterns,
	})
	if err != nil {
		b.Fatal(err)
	}
	var output strings.Builder
	for i := range patterns {
		fmt.Fprintf(&output, "section-%02d: value\n", i)
	}
	gradingContext := &Context{Output: output.String()}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := grader.Grade(context.Background(), gradingContext); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkToolConstraintRegexes(b *testing.B) {
	specs := make([]models.ToolSpecParameters, 12)
	for i := range specs {
		specs[i] = models.ToolSpecParameters{
			Tool:           fmt.Sprintf(`^tool-%02d$`, i),
			CommandPattern: `^run --mode=(fast|safe)$`,
			PathPattern:    `^src/[a-z]+\.go$`,
		}
	}
	grader, err := NewToolConstraintGrader("tools", models.ToolConstraintGraderParameters{
		ExpectTools: specs,
	})
	if err != nil {
		b.Fatal(err)
	}
	calls := make([]models.ToolCall, 96)
	for i := range calls {
		calls[i] = models.ToolCall{
			Name: fmt.Sprintf("tool-%02d", i%len(specs)),
			Arguments: models.ToolCallArgs{
				Command: "run --mode=fast",
				Path:    "src/main.go",
			},
		}
	}
	gradingContext := &Context{Session: &models.SessionDigest{ToolCalls: calls}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := grader.Grade(context.Background(), gradingContext); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFileGraderRegexes(b *testing.B) {
	directory := b.TempDir()
	path := filepath.Join(directory, "result.txt")
	var content strings.Builder
	patterns := make([]string, 16)
	for i := range patterns {
		patterns[i] = fmt.Sprintf(`(?m)^section-%02d: [a-z]+$`, i)
		fmt.Fprintf(&content, "section-%02d: value\n", i)
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	grader, err := NewFileGrader("file", models.FileGraderParameters{
		ContentPatterns: []models.FileContentPatternParameters{{
			Path:      "result.txt",
			MustMatch: patterns,
		}},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if failures := grader.checkContentPatterns(directory); len(failures) != 0 {
			b.Fatal(failures)
		}
	}
}
