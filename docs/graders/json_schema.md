### `json_schema` - JSON Schema Validation Grader

Validates that the agent output is valid JSON conforming to a given JSON schema.

```yaml
- type: json_schema
  name: api_response_format
  config:
    schema:
      type: object
      required:
        - status
        - data
      properties:
        status:
          type: string
          enum: ["success", "error"]
        data:
          type: object
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `schema` | object | Inline JSON schema for validation |
| `schema_file` | string | Path to a JSON schema file (used when `schema` is not provided) |
| `extract_json` | boolean | Extract exactly one JSON object or array from prose or a Markdown JSON code block before validation (default: `false`) |

One of `schema` or `schema_file` must be specified.

By default, the output must be pure JSON. Set `extract_json: true` when the
agent may add prose or wrap its response in a JSON code block. Extraction
requires exactly one JSON object or array; responses with zero or multiple
candidate JSON documents fail to avoid validating an absent or ambiguous
fragment.

**Scoring:** Binary — `1.0` if the output is valid JSON matching the schema, `0.0` otherwise.

**Validation Steps:**
1. Checks that the output is valid JSON
2. Resolves the schema (inline or from file)
3. Validates the parsed output against the schema

**Example: Schema from file**

```yaml
- type: json_schema
  name: config_format
  config:
    schema_file: schemas/config.json
```

**Example: Inline schema with nested objects**

```yaml
- type: json_schema
  name: deployment_result
  config:
    schema:
      type: object
      required:
        - url
        - status
      properties:
        url:
          type: string
          pattern: "^https://"
        status:
          type: string
        resources:
          type: array
          items:
            type: object
            required:
              - name
              - type
            properties:
              name:
                type: string
              type:
                type: string
```
