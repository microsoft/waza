package execution

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"github.com/microsoft/waza/internal/models"
)

// streamResult holds the parsed outcome of a `claude -p --output-format
// stream-json --verbose` run.
type streamResult struct {
	FinalOutput     string
	ClaudeSessionID string
	ToolCalls       []models.ToolCall
	Success         bool
	TotalCostUSD    float64
	Stderr          string // collected stderr, for error reporting
}

// streamEnvelope is the top-level NDJSON object emitted by the claude CLI in
// stream-json mode. Only the fields we consume are declared; encoding/json
// ignores unknown fields, so this stays robust to CLI additions.
type streamEnvelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system init + result events carry the session id.
	SessionID string `json:"session_id"`

	// result event fields.
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`

	// stream_event events carry a nested Anthropic streaming event.
	Event *streamInnerEvent `json:"event"`
}

// streamInnerEvent mirrors the Anthropic message-streaming events nested under
// {"type":"stream_event","event":{...}}.
type streamInnerEvent struct {
	Type         string            `json:"type"`
	ContentBlock *streamContentBlk `json:"content_block"`
	Delta        *streamDelta      `json:"delta"`
}

type streamContentBlk struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
}

// parseStream consumes the stream-json NDJSON on r (stdout) and concurrently
// drains stderrReader to avoid a pipe deadlock. It returns the accumulated
// result; the caller inspects the process exit status separately.
//
// Assumption: Anthropic streams content blocks sequentially (a block's
// content_block_start ... content_block_stop pair never interleaves with
// another block), so a single pending tool-call pointer is sufficient.
func parseStream(r io.Reader, stderrReader io.Reader) (streamResult, error) {
	var result streamResult

	// Drain stderr concurrently so the child never blocks writing to a full
	// stderr pipe while we read stdout.
	stderrCh := make(chan string, 1)
	go func() {
		if stderrReader == nil {
			stderrCh <- ""
			return
		}
		data, _ := io.ReadAll(stderrReader)
		stderrCh <- strings.TrimRight(string(data), " \t\r\n")
	}()

	var finalText strings.Builder

	// Pending tool call being accumulated between content_block_start and
	// content_block_stop.
	type pendingTool struct {
		name string
		args strings.Builder
	}
	var pending *pendingTool

	scanner := bufio.NewScanner(r)
	// stream-json lines can be large; raise the buffer ceiling well past the
	// default 64KiB token limit.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var env streamEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			// Skip lines we can't parse rather than aborting the whole stream;
			// a single malformed line shouldn't lose an otherwise good run.
			continue
		}

		switch env.Type {
		case "system":
			if env.Subtype == "init" && env.SessionID != "" {
				result.ClaudeSessionID = env.SessionID
			}
		case "stream_event":
			if env.Event == nil {
				continue
			}
			switch env.Event.Type {
			case "content_block_start":
				cb := env.Event.ContentBlock
				if cb == nil {
					continue
				}
				if cb.Type == "tool_use" {
					pending = &pendingTool{name: cb.Name}
				}
				// content_block.type == "text" needs no special handling; text
				// deltas are always accumulated below.
			case "content_block_delta":
				d := env.Event.Delta
				if d == nil {
					continue
				}
				switch d.Type {
				case "text_delta":
					finalText.WriteString(d.Text)
				case "input_json_delta":
					if pending != nil {
						pending.args.WriteString(d.PartialJSON)
					}
				}
			case "content_block_stop":
				if pending != nil {
					tc := models.ToolCall{
						Name:    pending.name,
						Success: true, // CLI-side execution assumed complete; no per-tool result here.
					}
					raw := pending.args.String()
					if raw != "" {
						// Best effort: malformed/empty args still yield a tool
						// call, just with empty Arguments.
						_ = json.Unmarshal([]byte(raw), &tc.Arguments)
					}
					result.ToolCalls = append(result.ToolCalls, tc)
					pending = nil
				}
			}
		case "result":
			result.FinalOutput = env.Result
			result.TotalCostUSD = env.TotalCostUSD
			result.Success = true
			if result.ClaudeSessionID == "" && env.SessionID != "" {
				result.ClaudeSessionID = env.SessionID
			}
		}
	}

	scanErr := scanner.Err()

	// Fall back to streamed assistant text when no explicit result event was
	// seen. Success stays as-is (only the result event flips it true).
	if result.FinalOutput == "" {
		result.FinalOutput = finalText.String()
	}

	result.Stderr = <-stderrCh

	return result, scanErr
}
