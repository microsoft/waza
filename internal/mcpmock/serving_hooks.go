package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/microsoft/waza/internal/jsonrpc"
)

// ToolCallDispatch is an opt-in internal serving hook, not public registration.
// It receives only tools/call requests with a native string, number, or explicit
// null ID. Before reserving a finite step it must decide whether it owns the tool.
// handled=true owns every outcome, including unmatched, exhausted, and canceled
// calls. Any error stops serving without legacy fallback, regardless of handled.
//
// Dispatch and deliver are synchronous: deliver must not be retained or invoked
// concurrently, and may be called at most once. A handled success requires a
// successful delivery; handled=false with no delivery delegates to the legacy
// handler. Requests must not be mutated. The callback requires the original
// native ID and writes the actual response; dispatch must propagate its error
// before recording delivery success. Simultaneous dispatch and delivery errors
// are joined so callers can inspect both causes.
type ToolCallDispatch func(ctx context.Context, request *jsonrpc.Request, deliver func(*jsonrpc.Response) error) (handled bool, err error)

// ServeStdioWithDispatch is a private, opt-in boundary around native transport
// writes. ServeStdio and HandleRequest retain their existing behavior. A nil
// dispatch uses the legacy handler and wire format. Non-native ID types bypass
// dispatch and retain the legacy handler's policy.
//
// Like ServeStdio, EOF (including a final unterminated line) stops reading without
// a response. Other read failures stop immediately, but are returned here rather
// than only logged; no synthetic protocol or tool result is emitted.
//
// Serving is serial. Context cancellation is checked between requests and before
// delivery; it cannot interrupt a blocked ReadRequest or WriteResponse. Callers
// own cancellation of synchronous dispatch and reader/writer I/O. This boundary
// does not implement concurrent requests or protocol cancellation notifications.
func ServeStdioWithDispatch(ctx context.Context, cfg *Config, r io.Reader, w io.Writer, logger *slog.Logger, dispatch ToolCallDispatch) error {
	srv := NewServer(cfg, logger)
	transport := jsonrpc.NewTransport(r, w)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("mcp mock serving canceled: %w", err)
		}
		req, rawJSON, err := transport.ReadRequest()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("mcp mock read request: %w", err)
		}
		// Notifications must never reserve a finite step or manufacture a result.
		if req.Method == "tools/call" && !hasIDField(rawJSON) {
			continue
		}
		if dispatch != nil && req.Method == "tools/call" && eligibleNativeID(req.ID) {
			handled, err := dispatchToolCall(ctx, transport, req, dispatch)
			if err != nil {
				return err
			}
			if handled {
				continue
			}
		}
		resp := srv.HandleRequest(ctx, req)
		if resp == nil || !hasIDField(rawJSON) {
			continue
		}
		if err := transport.WriteResponse(resp); err != nil {
			return fmt.Errorf("mcp mock write response: %w", err)
		}
	}
}

func eligibleNativeID(id json.RawMessage) bool {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch value.(type) {
	case nil, string, json.Number:
		return json.Valid(id)
	default:
		return false
	}
}

func dispatchToolCall(ctx context.Context, transport *jsonrpc.Transport, req *jsonrpc.Request, dispatch ToolCallDispatch) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("MCP dispatch canceled: %w", err)
	}
	id := bytes.Clone(req.ID)
	attempted := false
	active := true
	var deliveryErr error
	handled, err := dispatch(ctx, req, func(resp *jsonrpc.Response) error {
		if !active {
			return fmt.Errorf("MCP delivery callback used after dispatch returned")
		}
		if attempted {
			if deliveryErr == nil {
				deliveryErr = fmt.Errorf("MCP dispatch attempted multiple deliveries")
			}
			return deliveryErr
		}
		attempted = true
		if err := ctx.Err(); err != nil {
			deliveryErr = fmt.Errorf("MCP delivery canceled: %w", err)
		} else if resp == nil || resp.JSONRPC != "2.0" ||
			!bytes.Equal(bytes.TrimSpace(resp.ID), bytes.TrimSpace(id)) ||
			(resp.Result == nil) == (resp.Error == nil) {
			deliveryErr = fmt.Errorf("MCP dispatch returned an invalid response; require matching native ID and exactly one result or error")
		} else if err := transport.WriteResponse(resp); err != nil {
			deliveryErr = fmt.Errorf("mcp mock write dispatched response: %w", err)
		}
		return deliveryErr
	})
	active = false
	// A dispatch cannot turn a failed transport write into serving success.
	if err != nil {
		err = fmt.Errorf("mcp mock dispatch tool call: %w", err)
	}
	if deliveryErr != nil || err != nil {
		return handled, errors.Join(deliveryErr, err)
	}
	if attempted && !handled {
		return false, fmt.Errorf("MCP dispatch delivered without owning the tool call")
	}
	if handled && !attempted {
		return true, fmt.Errorf("MCP dispatch owned the tool call without delivering a response")
	}
	return handled, nil
}
