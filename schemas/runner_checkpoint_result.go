package schemas

import _ "embed"

//go:embed runner-checkpoint-result-1.0.schema.json
var RunnerCheckpointResultSchemaJSON string

//go:embed runner-checkpoint-result-1.1.schema.json
var RunnerCheckpointResultV11SchemaJSON string
