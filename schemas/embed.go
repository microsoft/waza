package schemas

import _ "embed"

//go:embed eval.schema.json
var EvalSchemaJSON string

//go:embed task.schema.json
var TaskSchemaJSON string

//go:embed evidence-manifest-1.0.schema.json
var EvidenceManifestSchemaJSON string

//go:embed evidence-manifest-1.1.schema.json
var ReferenceInputEvidenceManifestSchemaJSON string

//go:embed grader-reference-input-1.0.schema.json
var GraderReferenceInputSchemaJSON string

//go:embed preflight.schema.json
var PreflightSchemaJSON string

//go:embed grader-reference-1.0.schema.json
var GraderReferenceSchemaJSON string

//go:embed grader-review-1.0.schema.json
var GraderReviewSchemaJSON string

//go:embed grader-assurance-1.0.schema.json
var GraderAssuranceSchemaJSON string

//go:embed grader-assurance-1.1.schema.json
var CalibratedGraderAssuranceSchemaJSON string
