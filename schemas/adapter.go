package schemas

import _ "embed"

//go:embed adapter-work-descriptor-1.0.schema.json
var AdapterWorkDescriptorSchemaJSON string

//go:embed adapter-translation-1.0.schema.json
var AdapterTranslationSchemaJSON string

//go:embed adapter-result-1.0.schema.json
var AdapterResultSchemaJSON string
