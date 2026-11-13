package schemas

import _ "embed"

//go:embed provenance-statement-1.0.schema.json
var ProvenanceStatementSchemaJSON string

//go:embed provenance-bundle-1.0.schema.json
var ProvenanceBundleSchemaJSON string

//go:embed provenance-verification-1.0.schema.json
var ProvenanceVerificationSchemaJSON string
