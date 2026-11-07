package evidencestate

import (
	"errors"
	"testing"

	"github.com/microsoft/waza/internal/models"
)

func TestRecordedConversationAndToolPresenceIsNotRepaired(t *testing.T) {
	report := capturedMockReport(t)
	terminal, binding := report.Terminal()
	for _, test := range []struct {
		name     string
		category Category
		payload  string
		state    State
		count    int64
		invalid  bool
	}{
		{"explicit empty messages", Conversation, `[]`, Present, 0, false},
		{"empty recorded text", Conversation, `[{"type":"user.message","content":""}]`, Present, 1, false},
		{"whitespace recorded text", Conversation, `[{"type":"assistant.message","content":" \n "}]`, Present, 1, false},
		{"missing text", Conversation, `[{"type":"user.message"}]`, Incomplete, 1, false},
		{"null text", Conversation, `[{"type":"assistant.message","content":null}]`, Incomplete, 1, false},
		{"boolean text", Conversation, `[{"type":"user.message","content":false}]`, Unavailable, 0, true},
		{"no inferred output", Conversation, `[{"type":"tool.execution_complete","content":"answer"}]`, Unavailable, 0, true},
		{"no reasoning message", Conversation, `[{"type":"assistant.reasoning","content":"text"}]`, Unavailable, 0, true},
		{"no streaming message", Conversation, `[{"type":"assistant.message_delta","content":"text"}]`, Unavailable, 0, true},
		{"null collection", Conversation, `null`, Unavailable, 0, true},
		{"duplicate content", Conversation, `[{"type":"user.message","content":"one","content":"two"}]`, Unavailable, 0, true},
		{"explicit empty tools", Tool, `[]`, Present, 0, false},
		{"recorded true", Tool, `[{"turn":0,"sequence":1,"tool_name":"test","success":true}]`, Present, 1, false},
		{"recorded failure", Tool, `[{"turn":1,"sequence":1,"tool_name":"test","success":false,"error":"failed"}]`, Present, 1, false},
		{"missing success", Tool, `[{"turn":1,"sequence":1,"tool_name":"test"}]`, Incomplete, 1, false},
		{"unfinished false", Tool, `[{"turn":1,"sequence":1,"tool_name":"test","success":false}]`, Incomplete, 1, false},
		{"empty error is not completion", Tool, `[{"turn":1,"sequence":1,"tool_name":"test","success":false,"error":""}]`, Incomplete, 1, false},
		{"null success", Tool, `[{"turn":1,"sequence":1,"tool_name":"test","success":null}]`, Unavailable, 0, true},
		{"overflow ordinal", Tool, `[{"turn":9223372036854775808,"sequence":1,"tool_name":"test","success":true}]`, Unavailable, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, count, err := categoryShape(test.category, []byte(test.payload), &terminal, binding)
			if test.invalid {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid original fields accepted: %v", err)
				}
			} else if err != nil || state != test.state || count != test.count {
				t.Fatalf("presence contract changed: %s, %d, %v", state, count, err)
			}
		})
	}
}

func TestCaptureRejectsUnsafeUsageMetadataBeforeStorage(t *testing.T) {
	for _, host := range []string{"user:password@example.test", "example.test/private", "example.test?token=secret"} {
		t.Run(host, func(t *testing.T) {
			report := capturedMockReport(t)
			report.Observation.Usage = &models.UsageStats{ProviderHost: host}
			p, err := CaptureReport(report, []Category{})
			if err != nil {
				if p != nil || !errors.Is(err, ErrInvalid) {
					t.Fatalf("unsafe metadata error classification changed: %v", err)
				}
				return
			}
			if _, exists := p.objects["execution-evidence-usage.json"]; exists {
				t.Fatal("unsafe metadata acquired a persisted usage payload")
			}
			for _, record := range p.index.Categories {
				if record.Category == Usage && record.State != Redacted {
					t.Fatal("unsafe metadata was neither rejected nor wholly withheld")
				}
			}
		})
	}
}

func TestCapturedUsagePreservesNativeZeroAndIncompleteCreditEvidence(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing credits", true: "recorded zero"}[complete], func(t *testing.T) {
			report := capturedMockReport(t)
			report.Observation.Usage = &models.UsageStats{}
			if complete {
				zero := 0.0
				report.Observation.Usage.AICredits = &zero
				report.Observation.Usage.ModelMetrics = map[string]models.ModelUsage{
					"recorded-model": {AICredits: &zero},
				}
			}
			p, err := CaptureReport(report, []Category{})
			if err != nil {
				t.Fatal(err)
			}
			backend, published := persistCaptureFixture(t, p)
			_, categories, err := InspectCategories(t.Context(), backend, published.EvidenceSHA256)
			if err != nil {
				t.Fatal(err)
			}
			for _, category := range categories {
				if category.Category != Usage {
					continue
				}
				expected := Incomplete
				if complete {
					expected = Present
				}
				if category.State != expected || category.Usage == nil || (category.Usage.AICredits != nil) != complete {
					t.Fatalf("missing/explicit-zero usage evidence was repaired: %+v", category)
				}
			}
		})
	}
}
