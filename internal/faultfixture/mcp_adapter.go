package faultfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync/atomic"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/microsoft/waza/internal/schemaloader"
)

type mcpStep struct {
	text  string
	fails bool
	delay int64
}

type mcpEntry struct {
	matcher mcpmock.Response
	steps   []mcpStep
	finite  bool
	dir     string
}

// MCPAdapter is unregistered private dispatch for one server/tool owner.
type MCPAdapter struct {
	envelope Envelope
	entries  []mcpEntry
}

// PendingMCPCall owns an irreversible allocation admitted by Prepare.
// Deliver may run once; copying a pointer does not permit another delivery.
type PendingMCPCall struct {
	called       atomic.Bool
	matcher      int
	index        int
	observedStep int
	step         mcpStep
	id           json.RawMessage
	observer     Observer
	admittedCtx  context.Context
}

func NewMCPAdapter(envelope Envelope, root string) (*MCPAdapter, error) {
	snapshot, err := snapshotEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	if snapshot.Kind != "mcp" || len(snapshot.Fixtures) > 0 {
		return nil, fmt.Errorf("MCP adapter requires MCP responses without CLI fixture snapshots")
	}
	adapter := &MCPAdapter{envelope: snapshot}
	for _, fragment := range snapshot.Responses {
		source, _, err := decodeSource(fragment.Data, fragment.Format)
		if err != nil {
			return nil, err
		}
		fields := maps.Clone(source)
		delete(fields, "sequence")
		data, err := json.Marshal(fields)
		if err != nil {
			return nil, fmt.Errorf("encoding MCP matcher: %w", err)
		}
		var matcher mcpmock.Response
		if err := json.Unmarshal(data, &matcher); err != nil {
			return nil, fmt.Errorf("decoding validated MCP matcher: %w", err)
		}
		sources, finite, err := sourceSteps(source)
		if err != nil {
			return nil, err
		}
		entry := mcpEntry{matcher: matcher, finite: finite}
		for _, step := range sources {
			data, err := json.Marshal(step)
			if err != nil {
				return nil, fmt.Errorf("encoding MCP step: %w", err)
			}
			var response mcpmock.Response
			if err := json.Unmarshal(data, &response); err != nil {
				return nil, fmt.Errorf("decoding validated MCP step: %w", err)
			}
			delay, err := stepDelay(step)
			if err != nil {
				return nil, err
			}
			value := mcpStep{delay: delay}
			if response.Error != "" {
				value.fails = true
				value.text = fmt.Sprintf("mcp mock %q tool %q fixture error: %s", snapshot.Name, snapshot.Tool, response.Error)
			} else {
				payload, err := json.Marshal(response.Return)
				if err != nil {
					return nil, fmt.Errorf("encoding MCP step payload: %w", err)
				}
				value.text = string(payload)
			}
			entry.steps = append(entry.steps, value)
		}
		adapter.entries = append(adapter.entries, entry)
	}
	namespace, err := bindConfiguration(root, snapshot)
	if err != nil {
		return nil, err
	}
	for index := range adapter.entries {
		if adapter.entries[index].finite {
			dir, err := responseState(namespace, index)
			if err != nil {
				return nil, err
			}
			adapter.entries[index].dir = dir
		}
	}
	return adapter, nil
}

func (adapter *MCPAdapter) Configuration() ([]byte, error) {
	return json.Marshal(adapter.envelope)
}

// Invoke delivers configured fixture errors as payloads. Operational failures
// never become success-shaped tool results or trigger another matcher.
// The synchronous delivery callback must implement cancellation of native I/O.
func (adapter *MCPAdapter) Invoke(ctx context.Context, request *jsonrpc.Request, observer Observer, deliver func(*jsonrpc.Response) error) error {
	if err := ctx.Err(); err != nil {
		return observationFailure(observer, -1, -1, err)
	}
	if deliver == nil {
		return observationFailure(observer, -1, -1, fmt.Errorf("MCP fault adapter requires a delivery callback"))
	}
	pending, err := adapter.Prepare(ctx, request, observer)
	if err != nil {
		return err
	}
	return pending.Deliver(ctx, deliver)
}

// Prepare validates and reserves synchronously so a native admission reader can
// allocate in request order before running cancellable delivery concurrently.
// Observer callbacks must be prompt or cooperate with the caller's cancellation.
func (adapter *MCPAdapter) Prepare(ctx context.Context, request *jsonrpc.Request, observer Observer) (*PendingMCPCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, observationFailure(observer, -1, -1, err)
	}
	if request == nil || request.Method != "tools/call" {
		return nil, observationFailure(observer, -1, -1, fmt.Errorf("MCP fault adapter requires tools/call"))
	}
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		return nil, observationFailure(observer, -1, -1, fmt.Errorf("decoding MCP fault request: %w", err))
	}
	arguments := map[string]any{}
	if len(params.Arguments) > 0 && !bytes.Equal(bytes.TrimSpace(params.Arguments), []byte("null")) {
		if err := json.Unmarshal(params.Arguments, &arguments); err != nil {
			return nil, observationFailure(observer, -1, -1, fmt.Errorf("MCP fault arguments must be a JSON object"))
		}
	}
	if params.Name != adapter.envelope.Tool {
		return nil, observationFailure(observer, -1, -1, ErrUnmatched)
	}
	id := bytes.Clone(request.ID)
	for index, entry := range adapter.entries {
		matched, err := entry.matcher.MatchesWithLoader(arguments, schemaloader.Offline{})
		if err != nil {
			return nil, observationFailure(observer, index, -1, err)
		}
		if !matched {
			continue
		}
		step, observedStep, err := reserveExecution(ctx, entry.dir, index, len(entry.steps), entry.finite, observer)
		if err != nil {
			return nil, err
		}
		return &PendingMCPCall{
			matcher: index, index: step, observedStep: observedStep,
			step: entry.steps[step], id: id, observer: observer, admittedCtx: ctx,
		}, nil
	}
	return nil, observationFailure(observer, -1, -1, ErrUnmatched)
}

// Deliver delays and emits once. Native I/O cancellation remains the caller's
// responsibility; successful emission cannot be undone by later cancellation.
func (pending *PendingMCPCall) Deliver(ctx context.Context, deliver func(*jsonrpc.Response) error) error {
	if pending == nil {
		return fmt.Errorf("MCP fault delivery requires a prepared call")
	}
	if !pending.called.CompareAndSwap(false, true) {
		return fmt.Errorf("prepared MCP fault delivery already attempted")
	}
	if deliver == nil {
		return observationFailure(pending.observer, pending.matcher, pending.observedStep, fmt.Errorf("MCP fault delivery requires a delivery callback"))
	}
	if err := pending.admittedCtx.Err(); err != nil {
		return observationFailure(pending.observer, pending.matcher, pending.observedStep, err)
	}
	deliveryCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(pending.admittedCtx, cancel)
	defer stop()
	defer cancel()
	err := deliverExecution(deliveryCtx, pending.matcher, pending.index, pending.observedStep,
		func(int) int64 { return pending.step.delay },
		func(int) error {
			result := map[string]any{
				"content": []map[string]string{{"type": "text", "text": pending.step.text}},
			}
			if pending.step.fails {
				result["isError"] = true
			}
			return deliver(&jsonrpc.Response{JSONRPC: "2.0", ID: bytes.Clone(pending.id), Result: result})
		}, pending.observer)
	if err != nil {
		return errors.Join(err, pending.admittedCtx.Err())
	}
	return nil
}
