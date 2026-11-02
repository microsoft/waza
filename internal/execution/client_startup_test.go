package execution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

type startupBarrierClient struct {
	CopilotClient
	start     func(context.Context) error
	starts    atomic.Int32
	stops     atomic.Int32
	forces    atomic.Int32
	force     func()
	authError error
	stopError error
}

func (c *startupBarrierClient) Start(ctx context.Context) error {
	c.starts.Add(1)
	return c.start(ctx)
}

func (c *startupBarrierClient) Stop() error {
	c.stops.Add(1)
	return c.stopError
}

func (c *startupBarrierClient) ForceStop() {
	c.forces.Add(1)
	if c.force != nil {
		c.force()
	}
}

func (c *startupBarrierClient) GetAuthStatus(context.Context) (*copilot.GetAuthStatusResponse, error) {
	return &copilot.GetAuthStatusResponse{IsAuthenticated: true}, c.authError
}

func startupTestEngine(t *testing.T, client CopilotClient, diagnostic bool) *CopilotEngine {
	t.Helper()
	t.Setenv("COPILOT_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
	opts := &CopilotEngineBuilderOptions{
		NewCopilotClient: func(*copilot.ClientOptions) CopilotClient { return client },
	}
	if diagnostic {
		opts.DiagnosticObserver = func(ExecutionDiagnostic) error { return nil }
	}
	return NewCopilotEngineBuilder("test", opts).Build()
}

func awaitStartup(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startup barrier did not settle")
	}
}

func startupResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Initialize did not return")
		return nil
	}
}

type startupWaitContext struct {
	context.Context
	registered chan struct{}
	once       sync.Once
}

func (c *startupWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.registered) })
	return c.Context.Done()
}

func TestCopilotEngineStartupPreCanceled(t *testing.T) {
	client := &startupBarrierClient{start: func(context.Context) error { t.Error("Start called"); return nil }}
	engine := startupTestEngine(t, client, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, engine.Initialize(ctx), context.Canceled)
	require.Zero(t, client.starts.Load())
	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestCopilotEngineStartupCancellationCleansPartialClient(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "diagnostic"}[diagnostic], func(t *testing.T) {
			entered, forced := make(chan struct{}), make(chan struct{})
			var forceOnce sync.Once
			releaseForce := func() { forceOnce.Do(func() { close(forced) }) }
			t.Cleanup(releaseForce)
			client := &startupBarrierClient{
				start: func(ctx context.Context) error {
					close(entered)
					// Model SDK startup I/O that requires ForceStop as well as cancellation.
					<-forced
					return ctx.Err()
				},
				force: releaseForce,
			}
			engine := startupTestEngine(t, client, diagnostic)
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- engine.Initialize(ctx) }()
			awaitStartup(t, entered)
			cancel()
			require.Error(t, startupResult(t, result))
			require.NoError(t, engine.Shutdown(context.Background()))
			require.EqualValues(t, 1, client.forces.Load())
			require.EqualValues(t, 1, client.stops.Load())
			require.Error(t, engine.Initialize(context.Background()), "cancellation cannot resurrect the client")
			require.EqualValues(t, 1, client.starts.Load())
		})
	}
}

func TestCopilotEngineStartupLifetimeIndependentOfCaller(t *testing.T) {
	var lifetime context.Context
	client := &startupBarrierClient{start: func(ctx context.Context) error { lifetime = ctx; return nil }}
	engine := startupTestEngine(t, client, false)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, engine.Initialize(ctx))
	cancel()
	require.NoError(t, lifetime.Err(), "SDK issue 668: a healthy child must outlive its startup caller")
	require.NoError(t, engine.Initialize(context.Background()))
	require.EqualValues(t, 1, client.starts.Load())
	require.NoError(t, engine.Shutdown(context.Background()))
	require.ErrorIs(t, lifetime.Err(), context.Canceled)
}

func TestCopilotEngineStartupHealthyInitializeIgnoresCanceledCaller(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "diagnostic"}[diagnostic], func(t *testing.T) {
			client := &startupBarrierClient{start: func(context.Context) error { return nil }}
			engine := startupTestEngine(t, client, diagnostic)
			require.NoError(t, engine.Initialize(context.Background()))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.NoError(t, engine.Initialize(ctx), "healthy initialization remains idempotent")
			require.EqualValues(t, 1, client.starts.Load())
			require.Zero(t, client.stops.Load())
			require.NoError(t, engine.Shutdown(context.Background()))
		})
	}
}

func TestCopilotEngineStartupFailurePersists(t *testing.T) {
	failure := errors.New("startup sentinel")
	client := &startupBarrierClient{start: func(context.Context) error { return failure }}
	engine := startupTestEngine(t, client, false)
	require.ErrorIs(t, engine.Initialize(context.Background()), failure)
	require.ErrorIs(t, engine.Initialize(context.Background()), failure)
	require.EqualValues(t, 1, client.starts.Load())
	require.NoError(t, engine.Shutdown(context.Background()))
	require.EqualValues(t, 1, client.stops.Load())
}

func TestCopilotEngineAuthenticationFailurePersists(t *testing.T) {
	failure := errors.New("authentication sentinel")
	client := &startupBarrierClient{start: func(context.Context) error { return nil }, authError: failure}
	engine := startupTestEngine(t, client, false)
	require.ErrorIs(t, engine.Initialize(context.Background()), failure)
	require.ErrorIs(t, engine.Initialize(context.Background()), failure)
	require.EqualValues(t, 1, client.starts.Load())
	require.EqualValues(t, 1, client.stops.Load(), "authentication failure cleans up immediately")
	require.NoError(t, engine.Shutdown(context.Background()))
	require.EqualValues(t, 2, client.stops.Load(), "shutdown still performs final cleanup after auth failure")
}

func TestCopilotEngineStartupShutdownRejectsLateSuccess(t *testing.T) {
	for _, diagnostic := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "diagnostic"}[diagnostic], func(t *testing.T) {
			entered, forced := make(chan struct{}), make(chan struct{})
			var forceOnce sync.Once
			releaseForce := func() { forceOnce.Do(func() { close(forced) }) }
			t.Cleanup(releaseForce)
			client := &startupBarrierClient{
				start: func(context.Context) error { close(entered); <-forced; return nil },
				force: releaseForce,
			}
			engine := startupTestEngine(t, client, diagnostic)
			result := make(chan error, 1)
			go func() { result <- engine.Initialize(context.Background()) }()
			awaitStartup(t, entered)
			require.NoError(t, engine.Shutdown(context.Background()))
			require.Error(t, startupResult(t, result))
			require.Error(t, engine.Initialize(context.Background()))
			require.EqualValues(t, 1, client.stops.Load())
		})
	}
}

func TestCopilotEngineStartupSharedWaiterDoesNotCancelOwner(t *testing.T) {
	resetSharedClientForTest()
	t.Cleanup(resetSharedClientForTest)
	t.Setenv("COPILOT_CLI_PATH", "")
	t.Setenv("COPILOT_BASE_URL", "")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
	previousConstruct, previousPath := sharedConstruct, embeddedCLIPath
	t.Cleanup(func() { sharedConstruct, embeddedCLIPath = previousConstruct, previousPath })
	entered, release := make(chan struct{}), make(chan struct{})
	client := &startupBarrierClient{start: func(context.Context) error { close(entered); <-release; return nil }}
	sharedConstruct = func(*copilot.ClientOptions) CopilotClient { return client }
	embeddedCLIPath = func() (string, error) { return "unused-embedded-cli", nil }
	owner, waiter := NewCopilotEngineBuilder("owner", nil).Build(), NewCopilotEngineBuilder("waiter", nil).Build()
	ownerResult, waiterResult := make(chan error, 1), make(chan error, 1)
	go func() { ownerResult <- owner.Initialize(context.Background()) }()
	awaitStartup(t, entered)
	ctx, cancel := context.WithCancel(context.Background())
	waitContext := &startupWaitContext{Context: ctx, registered: make(chan struct{})}
	go func() { waiterResult <- waiter.Initialize(waitContext) }()
	awaitStartup(t, waitContext.registered)
	cancel()
	require.ErrorIs(t, startupResult(t, waiterResult), context.Canceled)
	require.Zero(t, client.forces.Load())
	require.NoError(t, waiter.Shutdown(context.Background()))
	close(release)
	require.NoError(t, startupResult(t, ownerResult))
	require.NoError(t, owner.Shutdown(context.Background()))
	require.Zero(t, client.stops.Load(), "engines do not own the shared client")
	require.NoError(t, ShutdownSharedClient(context.Background()))
	require.EqualValues(t, 1, client.stops.Load())
}

func TestCopilotEngineStartupCleanupBoundedForBrokenCustomClient(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	client := &startupBarrierClient{start: func(context.Context) error { close(entered); <-release; return nil }}
	engine := startupTestEngine(t, client, false)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- engine.Initialize(ctx) }()
	awaitStartup(t, entered)
	canceledAt := time.Now()
	cancel()
	initErr := startupResult(t, result)
	require.ErrorIs(t, initErr, context.Canceled)
	require.ErrorContains(t, initErr, "startup cleanup exceeded")
	require.Less(t, time.Since(canceledAt), 3*time.Second, "fixed caller budget, including scheduler margin")
	shutdownAt := time.Now()
	require.ErrorContains(t, engine.Shutdown(context.Background()), "startup cleanup exceeded")
	require.Less(t, time.Since(shutdownAt), 3*time.Second, "each cleanup caller has a fixed budget")
	releaseOnce.Do(func() { close(release) })
	managed, ok := engine.client.(*startupClient)
	require.True(t, ok)
	awaitStartup(t, managed.stopDone)
	require.EqualValues(t, 1, client.stops.Load(), "late completion is disposed")
}

func TestCopilotEngineStartupSharedCanceledOwnerCanBeReplaced(t *testing.T) {
	for _, mode := range []string{"cooperative", "late-success", "cleanup-failure"} {
		t.Run(mode, func(t *testing.T) {
			resetSharedClientForTest()
			t.Cleanup(resetSharedClientForTest)
			t.Setenv("COPILOT_CLI_PATH", "")
			t.Setenv("COPILOT_BASE_URL", "")
			t.Setenv("COPILOT_PROVIDER_BASE_URL", "")
			previousConstruct, previousPath := sharedConstruct, embeddedCLIPath
			t.Cleanup(func() { sharedConstruct, embeddedCLIPath = previousConstruct, previousPath })
			entered, forced, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			first := &startupBarrierClient{
				start: func(ctx context.Context) error {
					close(entered)
					if mode != "late-success" {
						select {
						case <-ctx.Done():
							return ctx.Err()
						case <-release:
							return errors.New("test cleanup released startup")
						}
					}
					<-release
					return nil
				},
				force: func() { close(forced) },
			}
			cleanupFailure := errors.New("shared startup cleanup sentinel")
			if mode == "cleanup-failure" {
				first.stopError = cleanupFailure
			}
			second := &startupBarrierClient{start: func(context.Context) error { return nil }}
			constructions := 0
			sharedConstruct = func(*copilot.ClientOptions) CopilotClient {
				constructions++
				if constructions == 1 {
					return first
				}
				return second
			}
			embeddedCLIPath = func() (string, error) { return "unused-embedded-cli", nil }
			old := NewCopilotEngineBuilder("old", nil).Build()
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- old.Initialize(ctx) }()
			awaitStartup(t, entered)
			cancel()
			if mode == "late-success" {
				awaitStartup(t, forced)
				require.Same(t, old.client, SharedClient(SharedClientOptions{}),
					"pending cleanup must remain tracked, not be discarded")
				require.Equal(t, 1, constructions)
				releaseOnce.Do(func() { close(release) })
			}
			initErr := startupResult(t, result)
			require.ErrorIs(t, initErr, context.Canceled)
			require.EqualValues(t, 1, first.stops.Load())
			if mode == "cleanup-failure" {
				require.ErrorIs(t, initErr, cleanupFailure)
				require.Same(t, old.client, SharedClient(SharedClientOptions{}),
					"failed cleanup must not be discarded by registry recovery")
				require.Equal(t, 1, constructions)
				require.ErrorIs(t, ShutdownSharedClient(context.Background()), cleanupFailure)
				require.EqualValues(t, 1, first.stops.Load())
				return
			}
			fresh := NewCopilotEngineBuilder("fresh", nil).Build()
			require.NotSame(t, old.client, fresh.client)
			require.Equal(t, 2, constructions)
			require.NoError(t, fresh.Initialize(context.Background()))
			require.Same(t, fresh.client, SharedClient(SharedClientOptions{}))
			require.Error(t, old.Initialize(context.Background()), "the abandoned instance cannot resurrect")
			require.NoError(t, old.Shutdown(context.Background()))
			require.Zero(t, second.stops.Load(), "retired engine does not own the replacement")
			require.NoError(t, fresh.Shutdown(context.Background()))
			require.NoError(t, ShutdownSharedClient(context.Background()))
			require.EqualValues(t, 1, first.stops.Load())
			require.EqualValues(t, 1, second.stops.Load())
			closed := SharedClient(SharedClientOptions{})
			require.ErrorIs(t, closed.Start(context.Background()), errSharedClientClosed)
			require.Equal(t, 2, constructions, "global shutdown cannot be bypassed by eviction")
		})
	}
}
