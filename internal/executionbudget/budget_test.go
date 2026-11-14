package executionbudget

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func ptr(v int64) *int64 { return &v }

func compile(t *testing.T, fragment string) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ExecutionBudgetSchemaJSON))
	require.NoError(t, err)
	require.NoError(t, compiler.AddResource("budget.json", doc))
	schema, err := compiler.Compile("budget.json" + fragment)
	require.NoError(t, err)
	return schema
}

func validate(t *testing.T, schema *jsonschema.Schema, value any) error {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	require.NoError(t, err)
	return schema.Validate(doc)
}

var (
	unixSupport    = measurementSupport{cpu: true, memory: true, cpuDetail: "cpu", memoryNote: "peak"}
	windowsSupport = measurementSupport{cpu: true, cpuDetail: "cpu"}
)

func TestCapabilityMatrixPerOS(t *testing.T) {
	schema := compile(t, "")
	for _, tc := range []struct {
		os             string
		support        measurementSupport
		cpu, memory    string
		cpuScope, memS string
	}{
		{"linux", unixSupport, Measured, Measured, ScopeRunnerProcess, ScopeRunnerProcess},
		{"darwin", unixSupport, Measured, Measured, ScopeRunnerProcess, ScopeRunnerProcess},
		{"windows", windowsSupport, Measured, Unsupported, ScopeRunnerProcess, ""},
		{"plan9", measurementSupport{}, Unsupported, Unsupported, "", ""},
	} {
		t.Run(tc.os, func(t *testing.T) {
			caps := capabilitiesFor(tc.os, "amd64", tc.support)
			require.NoError(t, validate(t, schema, caps))
			got := map[Resource]Capability{}
			for _, row := range caps.Resources {
				got[row.Resource] = row
			}
			require.Len(t, got, len(Resources))
			for _, r := range []Resource{WallTime, ModelTurns, ToolCalls} {
				require.Equal(t, Enforced, got[r].Enforcement)
				require.Equal(t, ScopeAttempt, got[r].Scope)
			}
			for _, r := range []Resource{CPUTime, Memory, Disk, ProcessCount} {
				require.Equal(t, Unsupported, got[r].Enforcement, "no OS quota is owned for %s", r)
			}
			require.Equal(t, tc.cpu, got[CPUTime].Measurement)
			require.Equal(t, tc.cpuScope, got[CPUTime].Scope)
			require.Equal(t, tc.memory, got[Memory].Measurement)
			require.Equal(t, tc.memS, got[Memory].Scope)
			require.Equal(t, Unsupported, got[Disk].Measurement)
			require.Equal(t, Unsupported, got[ProcessCount].Measurement)
		})
	}
	require.NoError(t, validate(t, schema, HostCapabilities()))
}

func TestPlanRejectsEachUnsupportedLimitBeforeExecution(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	reportSchema := compile(t, "#/$defs/report")
	for _, tc := range []struct {
		resource Resource
		limits   Limits
	}{
		{CPUTime, Limits{CPUTimeMS: ptr(10)}},
		{Memory, Limits{MemoryBytes: ptr(1 << 20)}},
		{Disk, Limits{DiskBytes: ptr(1 << 20)}},
		{ProcessCount, Limits{ProcessCount: ptr(4)}},
	} {
		t.Run(string(tc.resource), func(t *testing.T) {
			report, unsupported := Plan(Request{Limits: tc.limits}, caps)
			require.Equal(t, StateRejectedUnsupported, report.State)
			require.Equal(t, PolicyReject, report.Unsupported)
			require.Len(t, unsupported, 1)
			require.Contains(t, unsupported[0], string(tc.resource))
			require.Contains(t, unsupported[0], "linux/amd64")
			require.NoError(t, validate(t, reportSchema, report))

			report, unsupported = Plan(Request{Limits: tc.limits, Unsupported: PolicyReport}, caps)
			require.Equal(t, StateNotStarted, report.State)
			require.Len(t, unsupported, 1)
		})
	}
	report, unsupported := Plan(Request{Limits: Limits{WallTimeMS: ptr(1000), ModelTurns: ptr(2), ToolCalls: ptr(0)}}, caps)
	require.Empty(t, unsupported)
	require.Equal(t, StateNotStarted, report.State)
	require.Contains(t, Summary([]string{"x"}), `budgets.unsupported to "report"`)
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestMeterCountedLimitsCancelOnFirstOverLimitStart(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	for _, tc := range []struct {
		resource Resource
		limits   Limits
		observe  func(*Meter)
	}{
		{ModelTurns, Limits{ModelTurns: ptr(1)}, (*Meter).ModelTurnStarted},
		{ToolCalls, Limits{ToolCalls: ptr(1)}, (*Meter).ToolCallStarted},
	} {
		t.Run(string(tc.resource), func(t *testing.T) {
			plan, _ := Plan(Request{Limits: tc.limits}, caps)
			samples := []sample{{cpuMS: 100, peakRSS: 10, cpu: true, memory: true}, {cpuMS: 130, peakRSS: 20, cpu: true, memory: true}}
			clock := &fakeClock{now: time.Unix(0, 0)}
			ctx, meter := start(context.Background(), *plan, func() sample { s := samples[0]; samples = samples[1:]; return s }, clock.Now)
			tc.observe(meter)
			require.NoError(t, ctx.Err())
			tc.observe(meter)
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			var exceeded *ExceededError
			require.ErrorAs(t, context.Cause(ctx), &exceeded)
			require.Equal(t, Termination{Resource: tc.resource, Limit: 1, Observed: 2}, exceeded.Termination)
			require.ErrorIs(t, meter.Err(), context.Canceled)
			tc.observe(meter)
			clock.advance(250 * time.Millisecond)

			report := meter.Finish()
			require.NoError(t, validate(t, compile(t, "#/$defs/report"), report))
			require.Equal(t, StateTerminated, report.State)
			require.Equal(t, int64(2), report.Termination.Observed, "later starts do not inflate the termination")
			rows := map[Resource]Usage{}
			for _, row := range report.Resources {
				rows[row.Resource] = row
			}
			require.Equal(t, int64(250), *rows[WallTime].Used)
			require.Equal(t, int64(30), *rows[CPUTime].Used)
			require.Equal(t, int64(20), *rows[Memory].Used)
			require.True(t, *rows[tc.resource].Exceeded)
			require.Equal(t, int64(0), *rows[tc.resource].Remaining)
			require.Nil(t, rows[Disk].Used)
			tc.observe(meter)
		})
	}
}

func TestMeterWallTimeTerminatesAsDeadline(t *testing.T) {
	plan, _ := Plan(Request{Limits: Limits{WallTimeMS: ptr(10)}}, capabilitiesFor("linux", "amd64", unixSupport))
	ctx, meter := Start(context.Background(), *plan)
	<-ctx.Done()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.ErrorIs(t, meter.Err(), context.DeadlineExceeded)
	report := meter.Finish()
	require.Equal(t, StateTerminated, report.State)
	require.Equal(t, WallTime, report.Termination.Resource)
	require.GreaterOrEqual(t, report.Termination.Observed, int64(10))
}

func TestMeterFinishEnforcesPassedWallDeadline(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	clock := &fakeClock{now: time.Unix(0, 0)}
	plan, _ := Plan(Request{Limits: Limits{WallTimeMS: ptr(60_000)}}, caps)
	ctx, meter := start(context.Background(), *plan, func() sample { return sample{} }, clock.Now)
	clock.advance(61 * time.Second)
	report := meter.Finish()
	require.Equal(t, &Termination{Resource: WallTime, Limit: 60_000, Observed: 61_000}, report.Termination,
		"a passed deadline whose timer has not fired still ends the attempt")
	require.ErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)
	require.NoError(t, report.CheckTerminal("timeout"))

	plan, _ = Plan(Request{Limits: Limits{WallTimeMS: ptr(60_000), ModelTurns: ptr(0)}}, caps)
	clock = &fakeClock{now: time.Unix(0, 0)}
	_, meter = start(context.Background(), *plan, func() sample { return sample{} }, clock.Now)
	meter.ModelTurnStarted()
	clock.advance(61 * time.Second)
	require.Equal(t, ModelTurns, meter.Finish().Termination.Resource, "an earlier counted stop keeps its cause")

	enclosing, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	plan, _ = Plan(Request{Limits: Limits{WallTimeMS: ptr(60_000)}}, caps)
	clock = &fakeClock{now: time.Unix(0, 0)}
	_, meter = start(enclosing, *plan, func() sample { return sample{} }, clock.Now)
	clock.advance(61 * time.Second)
	report = meter.Finish()
	require.Nil(t, report.Termination, "an earlier request deadline is not attributed to the budget")
	require.Equal(t, StateExceeded, report.State)
}

func TestPreemptedMeterReportsLaterCrossings(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	clock := &fakeClock{now: time.Unix(0, 0)}
	plan, _ := Plan(Request{Limits: Limits{WallTimeMS: ptr(60_000), ToolCalls: ptr(0)}}, caps)
	ctx, meter := start(context.Background(), *plan, func() sample { return sample{} }, clock.Now)
	meter.Preempt()
	meter.ToolCallStarted()
	clock.advance(61 * time.Second)
	require.NoError(t, ctx.Err(), "a preempted attempt is not canceled by a later crossing")
	report := meter.Finish()
	require.Nil(t, report.Termination)
	require.Equal(t, StateExceeded, report.State)
	require.NoError(t, report.CheckTerminal("timeout"))

	plan, _ = Plan(Request{Limits: Limits{ToolCalls: ptr(0)}}, caps)
	_, meter = start(context.Background(), *plan, func() sample { return sample{} }, time.Now)
	meter.ToolCallStarted()
	meter.Preempt()
	require.Equal(t, ToolCalls, meter.Finish().Termination.Resource, "an earlier budget stop is kept")
}

func TestMeterWithinBudgetAndMeasurementOnlyExceedance(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	plan, _ := Plan(Request{Limits: Limits{ModelTurns: ptr(3)}}, caps)
	_, meter := start(context.Background(), *plan, func() sample { return sample{cpu: true, memory: true} }, time.Now)
	meter.ModelTurnStarted()
	report := meter.Finish()
	require.Equal(t, StateWithinBudget, report.State)
	require.Nil(t, report.Termination)
	require.Equal(t, int64(2), *report.Resources[1].Remaining)

	plan, _ = Plan(Request{Limits: Limits{MemoryBytes: ptr(5)}, Unsupported: PolicyReport}, caps)
	ctx, meter := start(context.Background(), *plan, func() sample { return sample{peakRSS: 50, cpu: true, memory: true} }, time.Now)
	report = meter.Finish()
	require.Equal(t, StateExceeded, report.State, "measured but unenforced limits are reported, not enforced")
	require.Nil(t, report.Termination)
	require.NotErrorIs(t, context.Cause(ctx), context.DeadlineExceeded)

	plan, _ = Plan(Request{Limits: Limits{CPUTimeMS: ptr(5)}, Unsupported: PolicyReport}, caps)
	_, meter = start(context.Background(), *plan, func() sample { return sample{} }, time.Now)
	report = meter.Finish()
	require.Equal(t, Unsupported, report.Resources[3].Measurement, "unavailable samples are reported as unsupported")
	require.Nil(t, report.Resources[3].Used)
	require.NoError(t, validate(t, compile(t, "#/$defs/report"), report))
}

func TestMeterLateEventsDoNotClaimTermination(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	plan, _ := Plan(Request{Limits: Limits{WallTimeMS: ptr(5), ToolCalls: ptr(0)}}, caps)
	ctx, meter := Start(context.Background(), *plan)
	<-ctx.Done()
	meter.ToolCallStarted()
	report := meter.Finish()
	require.Equal(t, WallTime, report.Termination.Resource, "the wall deadline fired first")
	require.Equal(t, int64(1), *report.Resources[2].Used, "late usage is still counted")
	require.True(t, *report.Resources[2].Exceeded)
	require.NoError(t, report.CheckTerminal("timeout"))

	parent, cancel := context.WithCancel(context.Background())
	plan, _ = Plan(Request{Limits: Limits{ModelTurns: ptr(0)}}, caps)
	_, meter = Start(parent, *plan)
	cancel()
	meter.ModelTurnStarted()
	require.NoError(t, meter.Err())
	report = meter.Finish()
	require.Nil(t, report.Termination, "caller cancellation is not attributed to a budget")
	require.Equal(t, StateExceeded, report.State)
	require.NoError(t, report.CheckTerminal("cancelled")) //nolint:misspell // wire value
}

func TestCheckTerminalRejectsContradictions(t *testing.T) {
	caps := capabilitiesFor("linux", "amd64", unixSupport)
	plan, _ := Plan(Request{Limits: Limits{ModelTurns: ptr(0)}}, caps)
	_, meter := start(context.Background(), *plan, func() sample { return sample{} }, time.Now)
	meter.ModelTurnStarted()
	terminated := meter.Finish()
	require.NoError(t, terminated.CheckTerminal("cancelled")) //nolint:misspell // wire value
	require.Error(t, terminated.CheckTerminal("success"))
	require.Error(t, terminated.CheckTerminal("timeout"))

	mutate := func(change func(*Report)) *Report {
		copied := terminated.Clone()
		change(copied)
		return copied
	}
	for name, report := range map[string]*Report{
		"remaining arithmetic":           mutate(func(r *Report) { *r.Resources[1].Remaining = 5 }),
		"exceeded flag":                  mutate(func(r *Report) { *r.Resources[1].Exceeded = false }),
		"within with termination":        mutate(func(r *Report) { r.State = StateWithinBudget }),
		"terminated without termination": mutate(func(r *Report) { r.Termination = nil }),
		"duplicate resource":             mutate(func(r *Report) { r.Resources[2] = r.Resources[1] }),
		"used while unmeasured":          mutate(func(r *Report) { r.Resources[1].Measurement = Unsupported }),
		"termination without usage": mutate(func(r *Report) {
			r.Resources[1].Used, r.Resources[1].Remaining, r.Resources[1].Exceeded = nil, nil, nil
		}),
		"understated usage": mutate(func(r *Report) { *r.Resources[1].Used = 0 }),
		"measured row without usage": mutate(func(r *Report) {
			r.Resources[2].Used, r.Resources[2].Remaining, r.Resources[2].Exceeded = nil, nil, nil
		}),
	} {
		require.Error(t, report.CheckTerminal("cancelled"), name) //nolint:misspell // wire value
	}

	wallPlan, _ := Plan(Request{Limits: Limits{WallTimeMS: ptr(1)}}, caps)
	clock := &fakeClock{now: time.Unix(0, 0)}
	wallCtx, wallMeter := start(context.Background(), *wallPlan, func() sample { return sample{} }, clock.Now)
	<-wallCtx.Done()
	rounded := wallMeter.Finish()
	require.Equal(t, &Termination{Resource: WallTime, Limit: 1, Observed: 1}, rounded.Termination)
	require.Zero(t, *rounded.Resources[0].Used)
	require.NoError(t, rounded.CheckTerminal("timeout"), "wall time may round below its deadline")
	overstated := rounded.Clone()
	overstated.Termination.Observed = 5
	require.Error(t, overstated.CheckTerminal("timeout"))

	rejected, _ := Plan(Request{Limits: Limits{DiskBytes: ptr(1)}}, caps)
	require.NoError(t, rejected.CheckTerminal("unsupported_capability"))
	require.Error(t, rejected.CheckTerminal("success"))

	unstarted, _ := Plan(Request{Limits: Limits{ModelTurns: ptr(3)}}, caps)
	require.NoError(t, unstarted.CheckTerminal("invalid_input"))
	require.NoError(t, unstarted.CheckTerminal("infrastructure_failure"))
	for _, completed := range []string{"success", "evaluation_failure"} {
		require.Error(t, unstarted.CheckTerminal(completed), "an unstarted attempt cannot have completed")
	}
}

func TestExceededErrorClassification(t *testing.T) {
	require.True(t, errors.Is(&ExceededError{Termination: Termination{Resource: WallTime}}, context.DeadlineExceeded))
	require.True(t, errors.Is(&ExceededError{Termination: Termination{Resource: ToolCalls}}, context.Canceled))
}
