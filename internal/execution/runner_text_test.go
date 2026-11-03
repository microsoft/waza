package execution

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type runnerTextTransport struct {
	CopilotClient
	interrupted bool
}

func (c *runnerTextTransport) ForceStop() { c.interrupted = true }

func TestRunnerTextRuntimeAdmission(t *testing.T) {
	for _, control := range []string{"valid", "nil", "shared", "command-mocks", "no-diagnostics", "unmanaged", "no-force-stop"} {
		t.Run(control, func(t *testing.T) {
			transport := &runnerTextTransport{}
			engine := &CopilotEngine{
				ownsClient: true, disableCommandMocks: true,
				client: manageStartup(transport), diagnostics: &diagnosticState{},
			}
			switch control {
			case "nil":
				engine = nil
			case "shared":
				engine.ownsClient = false
			case "command-mocks":
				engine.disableCommandMocks = false
			case "no-diagnostics":
				engine.diagnostics = nil
			case "unmanaged":
				engine.client = transport
			case "no-force-stop":
				engine.client = manageStartup(&stubClient{})
			}
			err := engine.ValidateRunnerTextRuntime()
			if control == "valid" {
				require.NoError(t, err)
				engine.ForceStopRunnerText()
				require.True(t, transport.interrupted)
			} else {
				require.Error(t, err)
				require.False(t, transport.interrupted, "admission must not start or stop rejected transports")
			}
		})
	}
}
