package utils

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionToSlogDebugDisabled(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(old)
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	NewSessionToSlog()(copilot.SessionEvent{Data: &copilot.RawSessionEventData{EventType: copilot.SessionEventType("message")}})
	assert.Equal(t, 0, buf.Len())
}

func TestSessionToSlogDebugEnabled(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(old)
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	content := "hello"
	reasoningText := "reasoning"

	NewSessionToSlog()(copilot.SessionEvent{
		Data: &copilot.AssistantMessageData{
			Content:       content,
			ReasoningText: &reasoningText,
		},
	})

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "Event received", logEntry["msg"])
	assert.Equal(t, "assistant.message", logEntry["type"])
	assert.Equal(t, content, logEntry["content"])
	assert.Equal(t, reasoningText, logEntry["reasoningText"])
}

func TestAppendIf(t *testing.T) {
	attrs := []any{"existing", "value"}

	result := appendIf(attrs, "missing", (*int)(nil))
	assert.Equal(t, attrs, result)

	v := 7
	result = appendIf(attrs, "number", &v)
	assert.Equal(t, []any{"existing", "value", "number", 7}, result)
}

func TestSlogPrinting(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() {
		slog.SetDefault(old)
	})

	actualBuffer := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(actualBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	slogger := NewSessionToSlog()

	for e, err := range NewCopilotLogIterator(filepath.Join("testdata", "sample_events.jsonl")) {
		require.NoError(t, err)
		slogger(e)
	}

	expectedBytes, err := os.ReadFile(filepath.Join("testdata", "sample_events_slog.jsonl"))
	require.NoError(t, err)

	actual := decodeAll(t, actualBuffer.Bytes())
	expected := decodeAll(t, expectedBytes)

	require.Equal(t, len(expected), len(actual))

	for i := range expected {
		delete(expected[i], "time")
		delete(actual[i], "time")

		require.Equalf(t, expected[i], actual[i], "[%d]", i)
	}
}

func TestSlogAppendMapOfStringAnyIf(t *testing.T) {
	t.Run("all_empty", func(t *testing.T) {
		values := []any{
			nil,
			map[string]any{},
			map[string]bool{},
			"really, I'm a map",
		}

		for _, v := range values {
			var attrs []any

			attrs = appendMapOfStringAnyIf(attrs, v, "myfield")
			require.Empty(t, attrs)
		}
	})

	t.Run("normal", func(t *testing.T) {
		var attrs []any

		attrs = appendMapOfStringAnyIf(attrs, map[string]any{
			"astring": "world",
			"abool":   true,
		}, "mygroup")
		require.NotEmpty(t, attrs)

		g, ok := attrs[0].(slog.Attr)
		require.True(t, ok)
		require.Equal(t, "mygroup", g.Key)

		actual := g.Value.Group()

		sort.Slice(actual, func(i, j int) bool {
			return actual[i].Key < actual[j].Key
		})

		require.Equal(t, []slog.Attr{
			{
				Key:   "abool",
				Value: slog.BoolValue(true),
			},
			{
				Key:   "astring",
				Value: slog.StringValue("world"),
			},
		}, actual)
	})
}

func decodeAll(t *testing.T, buff []byte) []map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(buff))

	var all []map[string]any

	for {
		var ll map[string]any

		err := decoder.Decode(&ll)

		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err)
		all = append(all, ll)
	}

	return all
}
