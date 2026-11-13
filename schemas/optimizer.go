package schemas

import _ "embed"

//go:embed optimizer-capabilities-1.0.schema.json
var OptimizerCapabilitiesSchemaJSON string

//go:embed optimizer-adapter-request-1.0.schema.json
var OptimizerAdapterRequestSchemaJSON string

//go:embed optimizer-adapter-response-1.0.schema.json
var OptimizerAdapterResponseSchemaJSON string

//go:embed optimizer-report-1.0.schema.json
var OptimizerReportSchemaJSON string
