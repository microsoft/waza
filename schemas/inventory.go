package schemas

import _ "embed"

//go:embed component-inventory-1.0.schema.json
var ComponentInventorySchemaJSON string

//go:embed inventory-verification-1.0.schema.json
var InventoryVerificationSchemaJSON string
