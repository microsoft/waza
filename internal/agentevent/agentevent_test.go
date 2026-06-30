package agentevent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/microsoft/waza/internal/agentevent"
)

type stubTextPayload struct {
	text string
	has  bool
}

func (s stubTextPayload) Text() (string, bool) { return s.text, s.has }

func TestEvent_Text_UsesTextProvider(t *testing.T) {
	evt := agentevent.New(agentevent.KindAssistantMessage, stubTextPayload{text: "hi there", has: true})
	got, ok := evt.Text()
	assert.True(t, ok)
	assert.Equal(t, "hi there", got)
}

func TestEvent_Text_NoProviderReturnsFalse(t *testing.T) {
	type opaque struct{}
	evt := agentevent.New(agentevent.KindAssistantMessage, opaque{})
	got, ok := evt.Text()
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

func TestEvent_Text_NilRawReturnsFalse(t *testing.T) {
	evt := agentevent.New(agentevent.KindAssistantMessage, nil)
	got, ok := evt.Text()
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

func TestEvent_Text_ProviderMayReportAbsentContent(t *testing.T) {
	evt := agentevent.New(agentevent.KindAssistantMessage, stubTextPayload{has: false})
	got, ok := evt.Text()
	assert.False(t, ok)
	assert.Equal(t, "", got)
}

func TestEvent_KindAndRawPreserved(t *testing.T) {
	payload := stubTextPayload{text: "x", has: true}
	evt := agentevent.New(agentevent.KindUserMessage, payload)
	assert.Equal(t, agentevent.KindUserMessage, evt.Kind())
	raw, ok := evt.Raw().(stubTextPayload)
	assert.True(t, ok)
	assert.Equal(t, payload, raw)
}
