package graders

import (
	"context"
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func BenchmarkToolConstraintRegexes(b *testing.B) {
	specs := make([]models.ToolSpecParameters, 12)
	for i := range specs {
		specs[i] = models.ToolSpecParameters{
			Tool:           fmt.Sprintf(`^tool-%02d$`, i),
			CommandPattern: `^run --mode=(fast|safe)$`,
			PathPattern:    `^src/[a-z]+\.go$`,
		}
	}
	params := models.ToolConstraintGraderParameters{
		ExpectTools: specs,
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
		grader, err := NewToolConstraintGrader("tools", params)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := grader.Grade(context.Background(), gradingContext); err != nil {
			b.Fatal(err)
		}
	}
}
