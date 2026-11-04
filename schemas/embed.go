package schemas

import _ "embed"

//go:embed runner-negotiation-request-1.0.schema.json
var RunnerNegotiationRequestSchemaJSON string

//go:embed runner-negotiation-response-1.0.schema.json
var RunnerNegotiationResponseSchemaJSON string

//go:embed runner-request-1.0.schema.json
var RunnerRequestSchemaJSON string

//go:embed runner-capabilities-1.0.schema.json
var RunnerCapabilitiesSchemaJSON string

//go:embed runner-unit-1.0.schema.json
var RunnerUnitSchemaJSON string

//go:embed eval.schema.json
var EvalSchemaJSON string

//go:embed task.schema.json
var TaskSchemaJSON string

//go:embed evidence-manifest-1.0.schema.json
var EvidenceManifestSchemaJSON string

//go:embed judge-evidence-1.0.schema.json
var JudgeEvidenceSchemaJSON string

//go:embed evidence-manifest-1.1.schema.json
var ReferenceInputEvidenceManifestSchemaJSON string

//go:embed grader-reference-input-1.0.schema.json
var GraderReferenceInputSchemaJSON string

//go:embed preflight.schema.json
var PreflightSchemaJSON string

//go:embed release-artifacts-1.0.schema.json
var ReleaseArtifactsSchemaJSON string

//go:embed grader-reference-1.0.schema.json
var GraderReferenceSchemaJSON string

//go:embed grader-review-1.0.schema.json
var GraderReviewSchemaJSON string

//go:embed grader-assurance-1.0.schema.json
var GraderAssuranceSchemaJSON string

//go:embed grader-assurance-1.1.schema.json
var CalibratedGraderAssuranceSchemaJSON string

//go:embed grader-assurance-1.2.schema.json
var PreservedGraderAssuranceSchemaJSON string

//go:embed release-assurance-contract-1.0.schema.json
var ReleaseAssuranceContractSchemaJSON string

//go:embed release-assurance-attempt-1.0.schema.json
var ReleaseAssuranceAttemptSchemaJSON string

//go:embed release-assurance-ledger-1.0.schema.json
var ReleaseAssuranceLedgerSchemaJSON string

//go:embed release-assured-decision-1.0.schema.json
var ReleaseAssuredDecisionSchemaJSON string
