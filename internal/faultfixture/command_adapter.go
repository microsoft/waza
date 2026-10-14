package faultfixture

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/microsoft/waza/internal/commandmock"
	"github.com/microsoft/waza/internal/models"
)

// CommandOutput is delivered only after a configured response is ready.
type CommandOutput struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

type commandStep struct {
	response models.CommandMockResponse
	delay    int64
	output   CommandOutput
}

type commandEntry struct {
	matcher models.CommandMockResponse
	steps   []commandStep
	finite  bool
	state   string
}

// CommandAdapter owns an immutable, task-private command response configuration.
type CommandAdapter struct {
	envelope  Envelope
	workspace string
	entries   []commandEntry
}

func decodeCommandFields(fields object) (models.CommandMockResponse, error) {
	data, err := json.Marshal(fields)
	if err != nil {
		return models.CommandMockResponse{}, fmt.Errorf("encoding command response: %w", err)
	}
	var response models.CommandMockResponse
	// Use legacy JSON numeric semantics only after raw-source validation.
	if err := json.Unmarshal(data, &response); err != nil {
		return models.CommandMockResponse{}, fmt.Errorf("decoding command response: %w", err)
	}
	return response, nil
}

func commandEntries(envelope Envelope) ([]commandEntry, error) {
	if envelope.Kind != "command" {
		return nil, fmt.Errorf("command adapter requires command fault configuration")
	}
	entries := make([]commandEntry, 0, len(envelope.Responses))
	matchers := make([]models.CommandMockResponse, 0, len(envelope.Responses))
	for index, fragment := range envelope.Responses {
		source, _, err := decodeSource(fragment.Data, fragment.Format)
		if err != nil {
			return nil, fmt.Errorf("command response[%d]: %w", index, err)
		}
		steps, finite, err := sourceSteps(source)
		if err != nil {
			return nil, err
		}
		matcherFields := maps.Clone(source)
		delete(matcherFields, "sequence")
		matcher, err := decodeCommandFields(matcherFields)
		if err != nil {
			return nil, err
		}
		entry := commandEntry{matcher: matcher, finite: finite}
		for _, step := range steps {
			delay, err := stepDelay(step)
			if err != nil {
				return nil, err
			}
			fields := maps.Clone(step)
			delete(fields, "delay_ms")
			if finite {
				fields["args"] = json.RawMessage(`[]`)
			}
			response, err := decodeCommandFields(fields)
			if err != nil {
				return nil, err
			}
			entry.steps = append(entry.steps, commandStep{response: response, delay: delay})
		}
		matchers = append(matchers, matcher)
		entries = append(entries, entry)
	}
	if err := models.ValidateCommandMocks([]models.CommandMockConfig{{Name: envelope.Name, Responses: matchers}}); err != nil {
		return nil, err
	}
	return entries, nil
}

// MaterializeCommandEnvelope validates all sources before capturing declared
// fixture bytes once. Recompilation can then run without reading mutable files.
func MaterializeCommandEnvelope(envelope Envelope, baseDir string) (Envelope, error) {
	snapshot, err := snapshotEnvelope(envelope)
	if err != nil {
		return Envelope{}, err
	}
	entries, err := commandEntries(snapshot)
	if err != nil {
		return Envelope{}, err
	}
	if baseDir == "" {
		baseDir = "."
	}
	if snapshot.Fixtures == nil {
		snapshot.Fixtures = make(map[string][]byte)
	}
	for _, entry := range entries {
		for _, step := range entry.steps {
			name := step.response.Fixture
			if name == "" {
				continue
			}
			if _, captured := snapshot.Fixtures[name]; captured {
				continue
			}
			data, err := commandmock.ResponseOutput(step.response, baseDir)
			if err != nil {
				return Envelope{}, fmt.Errorf("capturing command fixture %q: %w", name, err)
			}
			snapshot.Fixtures[name] = data
		}
	}
	return snapshot, nil
}

// NewCommandAdapter builds every matcher and output before binding any state.
// Referenced fixtures must already be present in the private envelope.
// An empty workspace inherits the envelope's workspace when one is configured.
func NewCommandAdapter(envelope Envelope, root, workspace string) (*CommandAdapter, error) {
	snapshot, err := snapshotEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	entries, err := commandEntries(snapshot)
	if err != nil {
		return nil, err
	}
	if workspace == "" && snapshot.Workspace != "" {
		workspace = snapshot.Workspace
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving command adapter workspace: %w", err)
	}
	if snapshot.Workspace != "" {
		boundWorkspace, err := filepath.Abs(snapshot.Workspace)
		if err != nil {
			return nil, fmt.Errorf("resolving configured command adapter workspace: %w", err)
		}
		if boundWorkspace != workspace {
			return nil, fmt.Errorf("command adapter workspace differs from private configuration")
		}
	}
	snapshot.Workspace = workspace
	for index := range entries {
		for stepIndex := range entries[index].steps {
			step := &entries[index].steps[stepIndex]
			var stdout []byte
			if step.response.Fixture != "" {
				var captured bool
				stdout, captured = snapshot.Fixtures[step.response.Fixture]
				if !captured {
					return nil, fmt.Errorf("command response[%d] fixture %q is missing from private configuration", index, step.response.Fixture)
				}
			} else {
				stdout, err = commandmock.ResponseOutput(step.response, "")
				if err != nil {
					return nil, fmt.Errorf("building command response[%d] output: %w", index, err)
				}
			}
			step.output = CommandOutput{Stdout: slices.Clone(stdout), Stderr: step.response.Stderr, ExitCode: step.response.ExitCode}
		}
	}
	namespace, err := bindConfiguration(root, snapshot)
	if err != nil {
		return nil, err
	}
	for index := range entries {
		if entries[index].finite {
			entries[index].state, err = responseState(namespace, index)
			if err != nil {
				return nil, err
			}
		}
	}
	return &CommandAdapter{envelope: snapshot, workspace: workspace, entries: entries}, nil
}

// Configuration returns a fresh private JSON envelope for subprocess recompilation.
func (adapter *CommandAdapter) Configuration() ([]byte, error) {
	data, err := json.Marshal(adapter.envelope)
	if err != nil {
		return nil, fmt.Errorf("encoding command adapter configuration: %w", err)
	}
	return data, nil
}

// Invoke selects the first matching response, even when its sequence is exhausted.
// Undelivered responses never call deliver, and reservation is irreversible.
// The synchronous delivery callback must implement cancellation of native I/O.
func (adapter *CommandAdapter) Invoke(ctx context.Context, args []string, cwd string, observer Observer, deliver func(CommandOutput) error) error {
	if err := ctx.Err(); err != nil {
		return observationFailure(observer, -1, -1, err)
	}
	if deliver == nil {
		return observationFailure(observer, -1, -1, fmt.Errorf("command adapter requires a delivery callback"))
	}
	for index, entry := range adapter.entries {
		matched, err := commandmock.MatchResponse(entry.matcher, args, cwd, adapter.workspace)
		if err != nil {
			return observationFailure(observer, index, -1, err)
		}
		if !matched {
			continue
		}
		return execute(ctx, entry.state, index, len(entry.steps), entry.finite,
			func(step int) int64 { return entry.steps[step].delay },
			func(step int) error {
				output := entry.steps[step].output
				output.Stdout = slices.Clone(output.Stdout)
				return deliver(output)
			}, observer)
	}
	return observationFailure(observer, -1, -1, ErrUnmatched)
}
