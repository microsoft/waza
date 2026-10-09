package mcpmock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sync"

	"github.com/microsoft/waza/internal/jsonrpc"
)

const ownedMCPCancelNotification = "notifications/cancelled" //nolint:misspell // Native MCP method spelling.

// ToolCallPrepare performs prompt, cooperative serial admission, including any
// irreversible reservation, in native read order. A nil execute and nil error
// declines to the legacy handler; a nonnil execute owns the entire outcome.
// Any preparation or execution error fails closed without legacy fallback.
//
// Requests must not be mutated or retained. Execute must finish synchronously,
// cooperate with its context, and use deliver at most once, synchronously, without
// retaining it or invoking it concurrently. Only successful actual delivery may
// be recorded as delivered. Cancellation cannot undo a committed native write.
type ToolCallPrepare func(ctx context.Context, request *jsonrpc.Request) (execute func(context.Context, func(*jsonrpc.Response) error) error, err error)

type ownedMCPCall struct {
	ctx    context.Context
	cancel context.CancelFunc
	key    string
	id     json.RawMessage
}

type ownedMCPJob struct {
	call    *ownedMCPCall
	request *jsonrpc.Request
	execute func(context.Context, func(*jsonrpc.Response) error) error
}

type ownedMCPServing struct {
	ctx       context.Context
	server    *Server
	transport *jsonrpc.Transport
	prepare   ToolCallPrepare
	capacity  int

	mu       sync.Mutex
	active   map[string]*ownedMCPCall
	inFlight int
	workers  sync.WaitGroup
	jobs     chan ownedMCPJob
	failures chan error
	finished chan struct{}
	readDone chan struct{}
}

// ServeOwnedStdioWithPrepare is a separate private concurrent serving boundary;
// neither serial serving API nor the default handler is changed or registered.
// maxInFlight must be explicitly positive. All ID-bearing responses, including
// legacy/lifecycle responses, run in bounded workers; the reader never writes.
// Duplicate active native IDs and capacity overflow fail before preparation.
//
// Ownership transfers only after argument validation. The caller must supply
// distinct, nonaliasing endpoints whose concurrent Close promptly interrupts
// their blocked I/O and returns. Detectable identical endpoints and nil arguments
// are rejected; hidden aliases and arbitrary closer behavior cannot be verified.
// Preparation and execution must be prompt/cooperative as described above.
//
// Native MCP cancellation notifications target active native JSON-RPC IDs, never
// SDK identities. Missing and explicit null IDs differ; strings use their decoded
// value, while numbers use strict lexical json.Number keys (1 != 1.0). No float64
// conversion or SDK identity join is performed. Unknown/missing/invalid cancel
// targets are ignored. A matched cancellation cancels the pending call and
// triggers global fail-closed shutdown, with no fabricated cancellation payload.
//
// EOF, including the native reader's dropped final unterminated line, stops
// admission and drains accepted calls, retaining root cancellation supervision
// throughout the drain. Other read/worker errors fail closed.
// Shutdown cancels calls, closes endpoints outside the transport write lock, and
// joins the reader and all workers. Closure-induced I/O errors do not replace the
// initiating cause; endpoint Close errors are joined with it.
func ServeOwnedStdioWithPrepare(ctx context.Context, cfg *Config, r io.ReadCloser, w io.WriteCloser, logger *slog.Logger, prepare ToolCallPrepare, maxInFlight int) error {
	if err := validateOwnedMCPArguments(ctx, cfg, r, w, maxInFlight); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	serving := &ownedMCPServing{
		ctx: runCtx, server: NewServer(cfg, logger), transport: jsonrpc.NewTransport(r, w),
		prepare: prepare, capacity: maxInFlight, active: make(map[string]*ownedMCPCall),
		failures: make(chan error, 1), finished: make(chan struct{}, 1), readDone: make(chan struct{}),
		jobs: make(chan ownedMCPJob, maxInFlight),
	}
	serving.workers.Add(maxInFlight)
	for range maxInFlight {
		go serving.work()
	}
	go serving.read()
	cause := serving.supervise(ctx)
	cancel()
	// Close must not acquire the transport's write lock: a worker may hold it
	// while blocked in Write, and only closing the endpoint can release that I/O.
	readCloseErr := r.Close()
	writeCloseErr := w.Close()
	<-serving.readDone
	serving.workers.Wait()
	return errors.Join(cause, readCloseErr, writeCloseErr)
}

func validateOwnedMCPArguments(ctx context.Context, cfg *Config, r io.ReadCloser, w io.WriteCloser, capacity int) error {
	if ctx == nil || cfg == nil || capacity <= 0 {
		return fmt.Errorf("owned MCP serving requires a context, configuration, and positive explicit capacity")
	}
	reader, writer := reflect.ValueOf(r), reflect.ValueOf(w)
	for _, endpoint := range []reflect.Value{reader, writer} {
		if !endpoint.IsValid() {
			return fmt.Errorf("owned MCP serving requires nonnil, distinct interruptible endpoints")
		}
		switch endpoint.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			if endpoint.IsNil() {
				return fmt.Errorf("owned MCP serving requires nonnil, distinct interruptible endpoints")
			}
		}
	}
	if reader.Type() == writer.Type() && reader.Comparable() && reader.Interface() == writer.Interface() {
		return fmt.Errorf("owned MCP serving requires distinct, nonaliasing endpoints")
	}
	return nil
}

func (serving *ownedMCPServing) supervise(root context.Context) error {
	readerDone := serving.readDone
	draining := false
	for {
		if err := root.Err(); err != nil {
			return fmt.Errorf("owned MCP serving canceled: %w", err)
		}
		serving.mu.Lock()
		remaining := serving.inFlight
		serving.mu.Unlock()
		if draining && remaining == 0 {
			// Workers publish failures before removing their active entry.
			select {
			case err := <-serving.failures:
				return err
			default:
				return nil
			}
		}
		select {
		case <-root.Done():
			return fmt.Errorf("owned MCP serving canceled: %w", root.Err())
		case err := <-serving.failures:
			return err
		case <-readerDone:
			draining = true
			readerDone = nil
		case <-serving.finished:
		}
	}
}

func (serving *ownedMCPServing) fail(err error) {
	select {
	case serving.failures <- err:
	default:
	}
}

func (serving *ownedMCPServing) read() {
	defer close(serving.readDone)
	defer close(serving.jobs)
	for {
		if err := serving.ctx.Err(); err != nil {
			serving.fail(err)
			return
		}
		req, raw, err := serving.transport.ReadRequest()
		if err != nil {
			if err != io.EOF {
				serving.fail(fmt.Errorf("owned MCP read request: %w", err))
			}
			return
		}
		if !hasIDField(raw) {
			if req.Method == ownedMCPCancelNotification && serving.cancelNativeRequest(req.Params) {
				return
			}
			// Notifications, especially tools/call, never allocate or deliver.
			continue
		}
		call, err := serving.admit(req.ID)
		if err != nil {
			serving.fail(err)
			return
		}
		var execute func(context.Context, func(*jsonrpc.Response) error) error
		err = call.ctx.Err()
		if err == nil && serving.prepare != nil && req.Method == "tools/call" && eligibleNativeID(req.ID) {
			execute, err = serving.prepare(call.ctx, req)
		}
		if err != nil {
			serving.fail(fmt.Errorf("owned MCP prepare tool call: %w", err))
			serving.release(call)
			return
		}
		// Every queued job already holds a capacity slot, so this bounded queue
		// cannot fill beyond capacity or block admission on response output.
		serving.jobs <- ownedMCPJob{call: call, request: req, execute: execute}
	}
}

func (serving *ownedMCPServing) admit(id json.RawMessage) (*ownedMCPCall, error) {
	if err := serving.ctx.Err(); err != nil {
		return nil, err
	}
	key, native := ownedNativeIDKey(id)
	serving.mu.Lock()
	defer serving.mu.Unlock()
	if native && serving.active[key] != nil {
		return nil, fmt.Errorf("owned MCP serving rejected a duplicate active native request ID")
	}
	if serving.inFlight >= serving.capacity {
		return nil, fmt.Errorf("owned MCP serving exceeded its explicit in-flight capacity")
	}
	ctx, cancel := context.WithCancel(serving.ctx)
	call := &ownedMCPCall{ctx: ctx, cancel: cancel, id: bytes.Clone(id)}
	if native {
		call.key = key
		serving.active[key] = call
	}
	serving.inFlight++
	return call, nil
}

func (serving *ownedMCPServing) release(call *ownedMCPCall) {
	call.cancel()
	serving.mu.Lock()
	if call.key != "" {
		delete(serving.active, call.key)
	}
	serving.inFlight--
	serving.mu.Unlock()
	select {
	case serving.finished <- struct{}{}:
	default:
	}
}

func ownedNativeIDKey(id json.RawMessage) (string, bool) {
	if !eligibleNativeID(id) {
		return "", false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	switch value := value.(type) {
	case nil:
		return "null", true
	case string:
		return "string:" + value, true
	case json.Number:
		return "number:" + string(value), true
	default:
		return "", false
	}
}

func (serving *ownedMCPServing) cancelNativeRequest(params json.RawMessage) bool {
	var notification struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &notification); err != nil {
		return false
	}
	key, native := ownedNativeIDKey(notification.RequestID)
	if !native {
		return false
	}
	serving.mu.Lock()
	call := serving.active[key]
	if call != nil {
		call.cancel()
	}
	serving.mu.Unlock()
	if call == nil {
		return false
	}
	serving.fail(fmt.Errorf("owned MCP native request canceled: %w", call.ctx.Err()))
	return true
}

func (serving *ownedMCPServing) work() {
	defer serving.workers.Done()
	for job := range serving.jobs {
		serving.respond(job.call, job.request, job.execute)
	}
}

func (serving *ownedMCPServing) respond(call *ownedMCPCall, req *jsonrpc.Request, execute func(context.Context, func(*jsonrpc.Response) error) error) {
	defer serving.release(call)
	var err error
	if execute != nil {
		err = executeOwnedMCPCall(call.ctx, serving.transport, call.id, execute)
	} else if err = call.ctx.Err(); err == nil {
		resp := serving.server.HandleRequest(call.ctx, req)
		if resp != nil {
			err = serving.transport.WriteResponseContext(call.ctx, resp)
		}
	}
	if err != nil {
		serving.fail(fmt.Errorf("owned MCP response worker: %w", err))
	}
}

func executeOwnedMCPCall(ctx context.Context, transport *jsonrpc.Transport, id json.RawMessage, execute func(context.Context, func(*jsonrpc.Response) error) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	active, attempted := true, false
	var deliveryErr error
	err := execute(ctx, func(resp *jsonrpc.Response) error {
		if !active {
			return fmt.Errorf("owned MCP delivery callback used after execution returned")
		}
		if attempted {
			if deliveryErr == nil {
				deliveryErr = fmt.Errorf("owned MCP execution attempted multiple deliveries")
			}
			return deliveryErr
		}
		attempted = true
		if err := ctx.Err(); err != nil {
			deliveryErr = err
		} else if resp == nil || resp.JSONRPC != "2.0" ||
			!bytes.Equal(bytes.TrimSpace(resp.ID), bytes.TrimSpace(id)) ||
			(resp.Result == nil) == (resp.Error == nil) {
			deliveryErr = fmt.Errorf("owned MCP execution returned an invalid response; require original native ID and exactly one result or error")
		} else {
			deliveryErr = transport.WriteResponseContext(ctx, resp)
		}
		return deliveryErr
	})
	active = false
	if err != nil || deliveryErr != nil {
		return errors.Join(err, deliveryErr)
	}
	if !attempted {
		return fmt.Errorf("owned MCP execution completed without delivering a response")
	}
	return nil
}
