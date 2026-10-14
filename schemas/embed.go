package schemas

import _ "embed"

//go:embed eval.schema.json
var EvalSchemaJSON string

//go:embed task.schema.json
var TaskSchemaJSON string

//go:embed evidence-manifest-1.0.schema.json
var EvidenceManifestSchemaJSON string

//go:embed preflight.schema.json
var PreflightSchemaJSON string
