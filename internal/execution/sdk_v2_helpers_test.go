package execution

import (
	"net"
	"strconv"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/stretchr/testify/require"
)

// tcpRuntime connects a test client to an already-running fake runtime.
func tcpRuntime(t testing.TB, address string) copilot.TCPConnection {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return copilot.TCPConnection{Host: host, Port: port}
}

// spawnedRuntime returns the stdio runtime a production client was configured with.
func spawnedRuntime(t testing.TB, options *copilot.ClientOptions) copilot.SpawnedStdioConnection {
	t.Helper()
	require.NotNil(t, options)
	conn, ok := options.Runtime.(copilot.SpawnedStdioConnection)
	require.Truef(t, ok, "runtime connection = %T, want copilot.SpawnedStdioConnection", options.Runtime)
	return conn
}

// sessionProvider returns the custom provider selected by session inference
// options, asserting the v2 representation is internally consistent: a custom
// provider is the single named default and hosted CAPI is disabled, while
// hosted sessions carry neither.
func sessionProvider(t testing.TB, inference copilot.SessionInferenceOptions, github copilot.SessionGitHubOptions) *copilot.NamedProviderConfig {
	t.Helper()
	if inference.DefaultProvider == nil && len(inference.Providers) == 0 {
		require.Nil(t, github.Capi, "hosted sessions must not configure CAPI")
		return nil
	}
	require.NotNil(t, inference.DefaultProvider)
	require.NotNil(t, *inference.DefaultProvider)
	require.Len(t, inference.Providers, 1)
	require.Equal(t, **inference.DefaultProvider, inference.Providers[0].Name)
	require.NotNil(t, github.Capi)
	require.NotNil(t, github.Capi.Enabled)
	require.False(t, *github.Capi.Enabled, "custom providers must disable hosted CAPI")
	return &inference.Providers[0]
}

// defaultProviderRef builds the double pointer SDK v2 uses for an explicit default provider.
func defaultProviderRef(name string) **string {
	ref := copilot.String(name)
	return &ref
}

// useTCPRuntime points a production-built client at an external fake runtime.
// Spawn-only startup settings (logging and credentials) belong to the external
// runtime, so the SDK rejects them on TCP connections.
func useTCPRuntime(t testing.TB, options *copilot.ClientOptions, address string) {
	t.Helper()
	options.Runtime = tcpRuntime(t, address)
	options.Observability = copilot.ClientObservabilityOptions{}
	options.GitHub.Token, options.GitHub.UseLoggedInUser = "", nil
}
