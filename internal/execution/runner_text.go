package execution

import "fmt"

// ValidateRunnerTextRuntime checks ownership before the exact runner takes
// lifecycle responsibility. Cancellation must be able to kill the private SDK
// transport even when a pending RPC does not honor its caller's context.
func (e *CopilotEngine) ValidateRunnerTextRuntime() error {
	if e == nil || !e.ownsClient || !e.disableCommandMocks || !e.disableAmbientDiscovery || e.diagnostics == nil {
		return fmt.Errorf("runner text execution requires an owned diagnostic client with command mocks and ambient discovery disabled")
	}
	client, ok := e.client.(*startupClient)
	if !ok {
		return fmt.Errorf("runner text execution requires a managed owned client")
	}
	if failed, ok := client.CopilotClient.(*startupErrorClient); ok {
		return fmt.Errorf("constructing runner text runtime: %w", failed.err)
	}
	if _, ok := client.CopilotClient.(interface{ ForceStop() }); !ok {
		return fmt.Errorf("runner text execution requires a force-stoppable owned transport")
	}
	return nil
}

// ForceStopRunnerText interrupts only a previously admitted private transport.
// The SDK implementation kills its subprocess before acquiring its state lock.
func (e *CopilotEngine) ForceStopRunnerText() {
	client, ok := e.client.(*startupClient)
	if !ok || !e.ownsClient {
		return
	}
	if force, ok := client.CopilotClient.(interface{ ForceStop() }); ok {
		force.ForceStop()
	}
}
