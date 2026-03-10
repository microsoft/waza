package graders

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestJSONSchemaGrader_Basic(t *testing.T) {
	g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
		Schema: map[string]any{
			"type": "object",
		},
	})
	require.NoError(t, err)

	require.Equal(t, models.GraderKindJSONSchema, g.Kind())
	require.Equal(t, "test", g.Name())
}

func TestJSONSchemaGrader_Constructor(t *testing.T) {
	t.Run("requires schema or schema_file", func(t *testing.T) {
		_, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "must have either 'schema' or 'schema_file'")
	})
}

func TestJSONSchemaGrader_Grade(t *testing.T) {
	t.Run("valid JSON matching schema passes", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
					"age":  map[string]any{"type": "integer"},
				},
				"required": []any{"name"},
			},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: `{"name": "Alice", "age": 30}`,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
		require.Equal(t, "Output matches JSON schema", results.Feedback)
	})

	t.Run("invalid JSON fails with score 0", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			Schema: map[string]any{"type": "object"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "not json at all",
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "Output is not valid JSON")
	})

	t.Run("valid JSON not matching schema fails", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
				"required": []any{"name"},
			},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: `{"age": 30}`,
		})
		require.NoError(t, err)
		require.False(t, results.Passed)
		require.Equal(t, 0.0, results.Score)
		require.Contains(t, results.Feedback, "Schema validation failed")
	})

	t.Run("array output matching schema passes", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			Schema: map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: `["a", "b", "c"]`,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("schema_file loads schema from disk", func(t *testing.T) {
		tmpDir := t.TempDir()
		schemaPath := filepath.Join(tmpDir, "schema.json")

		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "integer"},
			},
			"required": []any{"id"},
		}
		schemaBytes, err := json.Marshal(schema)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(schemaPath, schemaBytes, 0o644))

		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			SchemaFile: schemaPath,
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: `{"id": 42}`,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})

	t.Run("missing schema_file returns error", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			SchemaFile: "/nonexistent/schema.json",
		})
		require.NoError(t, err)

		_, err = g.Grade(context.Background(), &Context{
			Output: `{"id": 1}`,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read schema file")
	})

	t.Run("duration is recorded", func(t *testing.T) {
		g, err := NewJSONSchemaGrader("test", models.JSONSchemaGraderParameters{
			Schema: map[string]any{"type": "object"},
		})
		require.NoError(t, err)

		results, err := g.Grade(context.Background(), &Context{
			Output: "{}",
		})
		require.NoError(t, err)
		require.GreaterOrEqual(t, results.DurationMs, int64(0))
	})
}

func TestJSONSchemaGrader_ViaCreate(t *testing.T) {
	t.Run("Create with GraderKindJSONSchema works", func(t *testing.T) {
		g, err := Create("from-create", models.JSONSchemaGraderParameters{
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
		})
		require.NoError(t, err)
		require.Equal(t, "from-create", g.Name())
		require.Equal(t, models.GraderKindJSONSchema, g.Kind())

		results, err := g.Grade(context.Background(), &Context{
			Output: `{"name": "test"}`,
		})
		require.NoError(t, err)
		require.True(t, results.Passed)
		require.Equal(t, 1.0, results.Score)
	})
}

// Ensure jsonSchemaGrader satisfies the Grader interface at compile time.
var _ Grader = (*jsonSchemaGrader)(nil)
