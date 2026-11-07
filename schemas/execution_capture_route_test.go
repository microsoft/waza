package schemas

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestExecutionCaptureSchemaEmbeddedAndPublicRouteParity(t *testing.T) {
	data, err := os.ReadFile("execution-evidence-capture-1.0.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(ExecutionEvidenceCaptureSchemaJSON)) {
		t.Fatal("native capture schema differs from the served source")
	}
	route, err := os.ReadFile("../site/src/pages/schemas/[schema].json.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(route)
	if strings.Count(text, "import evidenceCapture from '../../../../schemas/execution-evidence-capture-1.0.schema.json';") != 1 ||
		strings.Count(text, "{ params: { schema: 'execution-evidence-capture-1.0.schema' }, props: { schema: evidenceCapture } }") != 1 {
		t.Fatal("capture schema must use the actual shared static route exactly once")
	}
}
