package schemas

import _ "embed"

//go:embed execution-lifecycle-observation-1.0.schema.json
var ExecutionLifecycleObservationSchemaJSON string

//go:embed execution-lifecycle-inspection-1.0.schema.json
var ExecutionLifecycleInspectionSchemaJSON string

//go:embed execution-lifecycle-1.0.schema.json
var ExecutionLifecycleSchemaJSON string

//go:embed execution-lifecycle-source-inspection-1.0.schema.json
var ExecutionLifecycleSourceInspectionSchemaJSON string

//go:embed execution-lifecycle-source-1.0.schema.json
var ExecutionLifecycleSourceSchemaJSON string
