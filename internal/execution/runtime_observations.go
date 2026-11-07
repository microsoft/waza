package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

// Two lifecycle slots remain available for reservation and terminal evidence.
const maxOwnedRuntimeObservations = 126

var ErrRuntimeObservation = errors.New("owned runtime observation is unavailable or incomplete")

type RuntimeObservation struct {
	Sequence   int    `json:"sequence"`
	ObservedAt string `json:"observed_at"`
	Operation  string `json:"operation"`
	Scope      string `json:"scope"`
	Result     string `json:"result"`
}

type RuntimeObservationSnapshot struct {
	OwnedEngineBound bool                 `json:"owned_engine_bound"`
	Observations     []RuntimeObservation `json:"observations"`
	Dropped          uint64               `json:"dropped"`
	PendingAtSeal    int64                `json:"pending_at_seal"`
	ProcessExit      string               `json:"process_exit"`
	LatePolicy       string               `json:"late_observation_policy"`
}

// OwnedRuntimeObservations is one fresh invocation's bounded, non-I/O sink.
// It is not an owner capability, source authenticator, or process-exit observer.
type OwnedRuntimeObservations struct {
	mu       sync.Mutex
	bound    atomic.Bool
	sealed   atomic.Bool
	dropped  atomic.Uint64
	late     atomic.Uint64
	active   atomic.Int64
	records  []RuntimeObservation
	snapshot RuntimeObservationSnapshot
}

func NewOwnedRuntimeObservations() *OwnedRuntimeObservations {
	return &OwnedRuntimeObservations{records: make([]RuntimeObservation, 0, maxOwnedRuntimeObservations)}
}

func (o *OwnedRuntimeObservations) bind() bool {
	if !o.mu.TryLock() {
		return false
	}
	defer o.mu.Unlock()
	return !o.sealed.Load() && o.bound.CompareAndSwap(false, true)
}

func (o *OwnedRuntimeObservations) record(operation, scope, result string) {
	if o == nil {
		return
	}
	if o.sealed.Load() {
		o.late.Add(1)
		return
	}
	o.active.Add(1)
	locked := false
	defer func() {
		o.active.Add(-1)
		if locked {
			o.mu.Unlock()
		}
	}()
	// Engine/event callbacks cannot wait on publication, observers or another
	// engine lock. Contention is unavailable evidence, not silently lost success.
	if !o.mu.TryLock() {
		if o.sealed.Load() {
			o.late.Add(1)
		} else {
			o.dropped.Add(1)
		}
		return
	}
	locked = true
	if o.sealed.Load() {
		o.late.Add(1)
		return
	}
	if len(o.records) == maxOwnedRuntimeObservations {
		o.dropped.Add(1)
		return
	}
	o.records = append(o.records, RuntimeObservation{
		Sequence: len(o.records) + 1, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Operation: operation, Scope: scope, Result: result,
	})
}

func (o *OwnedRuntimeObservations) observeContext(ctx context.Context) {
	if o == nil {
		return
	}
	result := "canceled"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result = "deadline-exceeded"
	}
	o.record("cancellation-context-observed", "host-context", result)
}

func (o *OwnedRuntimeObservations) observeEvent(event copilot.SessionEvent) {
	if o == nil {
		return
	}
	if data, ok := event.Data.(*copilot.AbortData); ok && data != nil &&
		(event.AgentID == nil || *event.AgentID == "") {
		o.record("turn-abort-event-observed", "sdk-turn", "observed")
	}
}

// Seal freezes publication bytes, including unavailable/overflow evidence.
// Late callbacks are operator-only and can never rewrite this snapshot.
func (o *OwnedRuntimeObservations) Seal() (RuntimeObservationSnapshot, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.sealed.Swap(true) {
		pending := o.active.Load()
		o.snapshot = RuntimeObservationSnapshot{
			OwnedEngineBound: o.bound.Load(), Observations: slices.Clone(o.records), Dropped: o.dropped.Load(),
			PendingAtSeal: pending,
			ProcessExit:   "unknown", LatePolicy: "excluded-after-seal; retained-operator-counter",
		}
	}
	snapshot := o.snapshot
	snapshot.Observations = slices.Clone(snapshot.Observations)
	if snapshot.Dropped != 0 || snapshot.PendingAtSeal != 0 {
		return snapshot, fmt.Errorf("%w: %d observations dropped, %d callbacks unjoined at seal",
			ErrRuntimeObservation, snapshot.Dropped, snapshot.PendingAtSeal)
	}
	return snapshot, nil
}

func (o *OwnedRuntimeObservations) LateObservations() uint64 {
	return o.late.Load()
}

type observedRuntimeClient struct {
	CopilotClient
	observations *OwnedRuntimeObservations
}

func (c *observedRuntimeClient) Start(ctx context.Context) error {
	c.observations.record("client-start-invoked", "sdk-client", "invoked")
	err := c.CopilotClient.Start(ctx)
	c.observations.record("client-start-returned", "sdk-client", runtimeCallResult(err))
	return err
}

func (c *observedRuntimeClient) Stop() error {
	c.observations.record("client-stop-invoked", "sdk-client", "invoked")
	err := c.CopilotClient.Stop()
	c.observations.record("client-stop-returned", "sdk-client", runtimeCallResult(err))
	return err
}

type observedForceRuntimeClient struct {
	*observedRuntimeClient
	force interface{ ForceStop() }
}

func (c *observedForceRuntimeClient) ForceStop() {
	c.observations.record("force-stop-invoked", "sdk-client", "invoked")
	c.force.ForceStop()
	c.observations.record("force-stop-returned", "sdk-client", "returned")
}

func observeRuntimeClient(client CopilotClient, observations *OwnedRuntimeObservations) CopilotClient {
	observed := &observedRuntimeClient{CopilotClient: client, observations: observations}
	if force, ok := client.(interface{ ForceStop() }); ok {
		return &observedForceRuntimeClient{observedRuntimeClient: observed, force: force}
	}
	return observed
}

func runtimeCallResult(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}
