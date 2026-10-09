package faultfixture

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestPrivateAdapterProcess(t *testing.T) {
	path := os.Getenv("WAZA_PRIVATE_ADAPTER_CONFIGURATION")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	envelope, err := DecodeEnvelope(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	root := os.Getenv("WAZA_PRIVATE_ADAPTER_ROOT")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	selected := -1
	observer := func(_, step int, transition Transition) error {
		if transition == Reserved {
			selected = step
		}
		return nil
	}
	switch envelope.Kind {
	case "command":
		adapter, buildErr := NewCommandAdapter(envelope, root, envelope.Workspace)
		if buildErr != nil {
			err = buildErr
			break
		}
		err = adapter.Invoke(ctx, nil, envelope.Workspace, observer, func(CommandOutput) error { return nil })
	case "mcp":
		adapter, buildErr := NewMCPAdapter(envelope, root)
		if buildErr != nil {
			err = buildErr
			break
		}
		params, encodeErr := json.Marshal(map[string]any{"name": envelope.Tool, "arguments": map[string]any{}})
		if encodeErr != nil {
			err = encodeErr
			break
		}
		request := &jsonrpc.Request{JSONRPC: "2.0", Method: "tools/call", Params: params, ID: json.RawMessage(`0`)}
		err = adapter.Invoke(ctx, request, observer, func(response *jsonrpc.Response) error {
			if !bytes.Equal(response.ID, request.ID) {
				return fmt.Errorf("native request identity was not preserved")
			}
			return nil
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if _, err := fmt.Fprint(os.Stdout, selected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runPrivateAdapterChild(t *testing.T, root string, data []byte) (string, error) {
	t.Helper()
	path := t.TempDir() + string(os.PathSeparator) + "configuration.json"
	require.NoError(t, os.WriteFile(path, data, 0600))
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPrivateAdapterProcess$")
	command.Env = append(os.Environ(), "WAZA_PRIVATE_ADAPTER_CONFIGURATION="+path, "WAZA_PRIVATE_ADAPTER_ROOT="+root)
	output, err := command.CombinedOutput()
	require.NoError(t, ctx.Err(), "watchdog expiry cannot count as version rejection")
	return string(output), err
}

func TestPrivateAdapterSubprocessVersionAndConsumption(t *testing.T) {
	for _, kind := range []string{"command", "mcp"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			envelope := mcpAdapterEnvelope(`{"sequence":[{"return":"one"},{"return":"two"}]}`)
			if kind == "command" {
				envelope.Kind, envelope.Name, envelope.Tool = kind, "inspect", ""
				envelope.Responses = []Fragment{{Format: YAML, Data: []byte("args: []\nsequence:\n  - stdout: first\n    exit_code: 0\n  - stderr: denied\n    exit_code: 1\n")}}
				adapter, err := NewCommandAdapter(envelope, root, t.TempDir())
				require.NoError(t, err)
				data, err := adapter.Configuration()
				require.NoError(t, err)
				envelope, err = DecodeEnvelope(data)
				require.NoError(t, err)
			}
			data, err := json.Marshal(envelope)
			require.NoError(t, err)
			for want := range 2 {
				output, err := runPrivateAdapterChild(t, root, data)
				require.NoError(t, err, output)
				got, err := strconv.Atoi(output)
				require.NoError(t, err)
				require.Equal(t, want, got)
			}
			output, err := runPrivateAdapterChild(t, root, data)
			require.Error(t, err)
			require.Contains(t, output, "exhausted")
			output, err = runPrivateAdapterChild(t, t.TempDir(), data)
			require.NoError(t, err, output)
			require.Equal(t, "0", output)
			for _, header := range []string{
				"scenario: inventory\n",
				"schemaVersion: \"1.4\"\n",
				"schemaVersion: \"1.4\"\nscenario: inventory\n",
				"schemaVersion: \"2.1\"\nscenario: inventory\n",
				"schemaVersion: \"3.0\"\nscenario: inventory\n",
				"schemaVersion: \"2.0\"\n",
			} {
				invalid := envelope
				invalid.Eval = []byte(header)
				data, err := json.Marshal(invalid)
				require.NoError(t, err)
				rejectedRoot := t.TempDir()
				output, err := runPrivateAdapterChild(t, rejectedRoot, data)
				require.Error(t, err, header)
				require.NotEmpty(t, output)
				entries, err := os.ReadDir(rejectedRoot)
				require.NoError(t, err)
				require.Empty(t, entries, "version rejection must precede binding and allocation")
			}
		})
	}
}

func TestPrivateMCPSubprocessSeparatesToolOwners(t *testing.T) {
	root := t.TempDir()
	first := mcpAdapterEnvelope(`{"sequence":[{"return":"one"}]}`)
	second := mcpAdapterEnvelope(`{"sequence":[{"return":"one"},{"return":"two"}]}`)
	second.Tool = "other"
	for _, item := range []struct {
		envelope Envelope
		want     string
	}{{first, "0"}, {second, "0"}, {second, "1"}} {
		data, err := json.Marshal(item.envelope)
		require.NoError(t, err)
		output, err := runPrivateAdapterChild(t, root, data)
		require.NoError(t, err, output)
		require.Equal(t, item.want, output)
	}
}
