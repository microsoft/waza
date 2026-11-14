package schemas_test

import (
	"encoding/json"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
)

func TestEvalSchemaDefaultMatchesRuntime(t *testing.T) {
	var schema struct {
		Properties struct {
			SchemaVersion struct {
				Default string `json:"default"`
			} `json:"schemaVersion"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemas.EvalSchemaJSON), &schema))
	require.Equal(t, models.CurrentSchemaVersion, schema.Properties.SchemaVersion.Default)
}

func TestProductionIntegrationAssuranceSchemaIsEmbedded(t *testing.T) {
	var schema struct {
		ID string `json:"$id"`
	}
	require.NoError(t, json.Unmarshal([]byte(schemas.ProductionIntegrationAssuranceSchemaJSON), &schema))
	require.Equal(t, "https://microsoft.github.io/waza/schemas/production-integration-assurance-1.0.schema.json", schema.ID)
}
