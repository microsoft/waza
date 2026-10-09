package faultfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/stretchr/testify/require"
)

func commandEnvelopeForTest(sources ...string) Envelope {
	envelope := Envelope{
		Eval: []byte("schemaVersion: \"2.0\"\nscenario: private-command-test\n"),
		Kind: "command",
		Name: "private-cli",
	}
	for _, source := range sources {
		envelope.Responses = append(envelope.Responses, Fragment{Format: JSON, Data: []byte(source)})
	}
	return envelope
}

func commandAdapterForTest(t *testing.T, sources ...string) *CommandAdapter {
	t.Helper()
	root := t.TempDir()
	adapter, err := NewCommandAdapter(commandEnvelopeForTest(sources...), root, root)
	require.NoError(t, err)
	return adapter
}

func invokeCommandForTest(adapter *CommandAdapter, ctx context.Context, args []string, observer Observer) (CommandOutput, bool, error) {
	var output CommandOutput
	delivered := false
	err := adapter.Invoke(ctx, args, adapter.workspace, observer, func(value CommandOutput) error {
		output, delivered = value, true
		return nil
	})
	return output, delivered, err
}

func TestCommandAdapterFaultOutputs(t *testing.T) {
	tests := []struct {
		name   string
		steps  string
		output []CommandOutput
	}{
		{"transient recovery", `[{"stderr":"temporary unavailable","exit_code":75},{"stdout":{"ready":true},"exit_code":0}]`,
			[]CommandOutput{{Stderr: "temporary unavailable", ExitCode: 75}, {Stdout: []byte(`{"ready":true}`)}}},
		{"permanent failure", `[{"stderr":"offline","exit_code":1},{"stderr":"offline","exit_code":1}]`,
			[]CommandOutput{{Stderr: "offline", ExitCode: 1}, {Stderr: "offline", ExitCode: 1}}},
		{"denied", `[{"stderr":"permission denied","exit_code":13}]`, []CommandOutput{{Stderr: "permission denied", ExitCode: 13}}},
		{"malformed stdout is intentional", `[{"stdout":"{malformed"}]`, []CommandOutput{{Stdout: []byte("{malformed")}}},
		{"explicit empty success", `[{"exit_code":0},{"stdout":null},{"stdout":""}]`,
			[]CommandOutput{{}, {}, {Stdout: []byte{}}}},
		{"legacy numeric output", `[{"stdout":{"n":9007199254740993}}]`,
			[]CommandOutput{{Stdout: []byte(`{"n":9007199254740992}`)}}},
	}
	for _, test := range tests {
		for _, format := range []Format{JSON, YAML} {
			t.Run(test.name+"/"+string(format), func(t *testing.T) {
				envelope := commandEnvelopeForTest(`{"args":["read"],"sequence":` + test.steps + `}`)
				envelope.Responses[0] = Fragment{Format: format, Data: sourceForFormat(t, string(envelope.Responses[0].Data), format)}
				root := t.TempDir()
				adapter, err := NewCommandAdapter(envelope, root, root)
				require.NoError(t, err)
				var events []Transition
				var allocations []int
				observer := func(matcher, step int, transition Transition) error {
					require.Equal(t, 0, matcher)
					events = append(events, transition)
					if transition == Reserved {
						allocations = append(allocations, step)
					}
					return nil
				}
				for _, want := range test.output {
					got, delivered, err := invokeCommandForTest(adapter, context.Background(), []string{"read"}, observer)
					require.NoError(t, err)
					require.True(t, delivered)
					require.Equal(t, want, got)
				}
				for index := range test.output {
					require.Equal(t, index, allocations[index])
					require.Equal(t, Reserved, events[index*2])
					require.Equal(t, Delivered, events[index*2+1])
				}
				_, delivered, err := invokeCommandForTest(adapter, context.Background(), []string{"read"}, nil)
				require.ErrorIs(t, err, faultsequence.ErrExhausted)
				require.False(t, delivered)
			})
		}
	}
}

func TestCommandAdapterFirstMatchAndUnmatched(t *testing.T) {
	adapter := commandAdapterForTest(t,
		`{"args":["read"],"sequence":[{"stdout":"specific"}]}`,
		`{"args_regex":[".*"],"stdout":"fallback"}`,
	)
	_, delivered, err := invokeCommandForTest(adapter, context.Background(), []string{"secret-token", "private-value"}, func(matcher, step int, transition Transition) error {
		require.Equal(t, -1, matcher)
		require.Equal(t, -1, step)
		require.Equal(t, Failed, transition)
		return nil
	})
	require.ErrorIs(t, err, ErrUnmatched)
	require.NotContains(t, err.Error(), "secret-token")
	require.NotContains(t, err.Error(), "private-value")
	require.False(t, delivered)

	got, delivered, err := invokeCommandForTest(adapter, context.Background(), []string{"read"}, nil)
	require.NoError(t, err)
	require.True(t, delivered)
	require.Equal(t, "specific", string(got.Stdout))
	_, delivered, err = invokeCommandForTest(adapter, context.Background(), []string{"read"}, nil)
	require.ErrorIs(t, err, faultsequence.ErrExhausted)
	require.False(t, delivered)

	for range 2 {
		got, delivered, err = invokeCommandForTest(adapter, context.Background(), []string{"other"}, func(matcher, step int, transition Transition) error {
			require.Equal(t, 1, matcher)
			require.Equal(t, -1, step)
			require.Equal(t, Delivered, transition)
			return nil
		})
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, "fallback", string(got.Stdout))
	}
}

func TestCommandAdapterNativeMatchers(t *testing.T) {
	t.Setenv("WAZA_PRIVATE_MATCH_TEST", "")
	adapter := commandAdapterForTest(t, `{"args_regex":["read",".+"],"environment":{"WAZA_PRIVATE_MATCH_TEST":""},"workdir":"sub/./dir","sequence":[{"stdout":"matched"}]}`)
	require.ErrorIs(t, adapter.Invoke(context.Background(), []string{"read", "value"}, adapter.workspace, nil, func(CommandOutput) error {
		t.Fatal("wrong directory must not deliver")
		return nil
	}), ErrUnmatched)
	require.ErrorIs(t, adapter.Invoke(context.Background(), []string{"prefix-read", "value"}, filepath.Join(adapter.workspace, "sub", "dir"), nil, func(CommandOutput) error {
		t.Fatal("regex must stay anchored")
		return nil
	}), ErrUnmatched)
	var output CommandOutput
	require.NoError(t, adapter.Invoke(context.Background(), []string{"read", "value"}, filepath.Join(adapter.workspace, "sub", "dir"), nil, func(value CommandOutput) error {
		output = value
		return nil
	}))
	require.Equal(t, "matched", string(output.Stdout))
}

func TestCommandAdapterCancellationAndNilDelivery(t *testing.T) {
	t.Run("pre-canceled and nil delivery consume nothing", func(t *testing.T) {
		adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"first"}]}`)
		require.ErrorContains(t, adapter.Invoke(context.Background(), nil, adapter.workspace, nil, nil), "delivery callback")
		markers, err := os.ReadDir(adapter.entries[0].state)
		require.NoError(t, err)
		require.Empty(t, markers)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, adapter.Invoke(ctx, nil, adapter.workspace, func(matcher, step int, transition Transition) error {
			require.Equal(t, -1, matcher)
			require.Equal(t, -1, step)
			require.Equal(t, Canceled, transition)
			return nil
		}, nil), context.Canceled)
		for _, args := range [][]string{nil, {"unmatched"}} {
			_, delivered, err := invokeCommandForTest(adapter, ctx, args, func(matcher, step int, transition Transition) error {
				require.Equal(t, -1, matcher)
				require.Equal(t, -1, step)
				require.Equal(t, Canceled, transition)
				return nil
			})
			require.ErrorIs(t, err, context.Canceled)
			require.False(t, delivered)
		}
		markers, err = os.ReadDir(adapter.entries[0].state)
		require.NoError(t, err)
		require.Empty(t, markers)
		got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, "first", string(got.Stdout))
	})
	t.Run("cancel after reservation consumes step", func(t *testing.T) {
		adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"never"},{"stdout":"second"}]}`)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var events []Transition
		_, delivered, err := invokeCommandForTest(adapter, ctx, nil, func(matcher, step int, transition Transition) error {
			require.Equal(t, 0, matcher)
			require.Equal(t, 0, step)
			events = append(events, transition)
			if transition == Reserved {
				cancel()
			}
			return nil
		})
		require.ErrorIs(t, err, context.Canceled)
		require.False(t, delivered)
		require.Equal(t, []Transition{Reserved, Canceled}, events)
		got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, "second", string(got.Stdout))
	})
	t.Run("deadline during delay consumes step", func(t *testing.T) {
		adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"never","delay_ms":60000},{"stdout":"second"}]}`)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var events []Transition
		_, delivered, err := invokeCommandForTest(adapter, ctx, nil, func(_ int, step int, transition Transition) error {
			require.Equal(t, 0, step)
			events = append(events, transition)
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, delivered)
		require.Equal(t, []Transition{Reserved, Canceled}, events)
		got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, "second", string(got.Stdout))
	})
	t.Run("completed delay delivers", func(t *testing.T) {
		adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"ready","delay_ms":1}]}`)
		got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, "ready", string(got.Stdout))
	})
}

func TestCommandAdapterCallbackErrorsAreIrreversible(t *testing.T) {
	sentinel := errors.New("callback failed")
	for _, failure := range []string{"reserved observer", "failed observer", "writer", "delivered observer"} {
		t.Run(failure, func(t *testing.T) {
			adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"first"},{"stdout":"second"}]}`)
			var events []Transition
			deliveries := 0
			err := adapter.Invoke(context.Background(), nil, adapter.workspace, func(_ int, step int, transition Transition) error {
				require.Equal(t, 0, step)
				events = append(events, transition)
				if failure == "reserved observer" && transition == Reserved ||
					failure == "delivered observer" && transition == Delivered ||
					failure == "failed observer" && transition == Failed {
					return sentinel
				}
				return nil
			}, func(CommandOutput) error {
				deliveries++
				if failure == "writer" || failure == "failed observer" {
					return errors.Join(sentinel, errors.New("writer refused output"))
				}
				return nil
			})
			require.ErrorIs(t, err, sentinel)
			if failure == "reserved observer" {
				require.Zero(t, deliveries)
			} else {
				require.Equal(t, 1, deliveries)
			}
			if failure == "delivered observer" {
				require.Equal(t, []Transition{Reserved, Delivered}, events)
			} else {
				require.Equal(t, []Transition{Reserved, Failed}, events)
			}
			got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
			require.NoError(t, err)
			require.True(t, delivered)
			require.Equal(t, "second", string(got.Stdout))
		})
	}
}

func TestCommandAdapterConcurrentAllocationAndAttemptReset(t *testing.T) {
	const count = 32
	steps := make([]string, count)
	for index := range steps {
		steps[index] = fmt.Sprintf(`{"stdout":"%d"}`, index)
	}
	envelope := commandEnvelopeForTest(`{"args":[],"sequence":[` + strings.Join(steps, ",") + `]}`)
	root := t.TempDir()
	first, err := NewCommandAdapter(envelope, root, root)
	require.NoError(t, err)
	config, err := first.Configuration()
	require.NoError(t, err)
	childEnvelope, err := DecodeEnvelope(config)
	require.NoError(t, err)
	second, err := NewCommandAdapter(childEnvelope, root, root)
	require.NoError(t, err)
	adapters := []*CommandAdapter{first, second}
	var mu sync.Mutex
	outputs := make(map[string]int)
	allocations := make(map[int]int)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for index := range count {
		wg.Go(func() {
			adapter := adapters[index%2]
			output, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, func(_ int, step int, transition Transition) error {
				if transition == Reserved {
					mu.Lock()
					allocations[step]++
					mu.Unlock()
				}
				return nil
			})
			if err != nil {
				failures <- err
			} else if !delivered {
				failures <- errors.New("allocated response was not delivered")
			} else {
				mu.Lock()
				outputs[string(output.Stdout)]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.Len(t, allocations, count)
	require.Len(t, outputs, count)
	for index := range count {
		require.Equal(t, 1, allocations[index])
		require.Equal(t, 1, outputs[fmt.Sprint(index)])
	}
	_, delivered, err := invokeCommandForTest(second, context.Background(), nil, nil)
	require.ErrorIs(t, err, faultsequence.ErrExhausted)
	require.False(t, delivered)

	resetRoot := t.TempDir()
	reset, err := NewCommandAdapter(envelope, resetRoot, resetRoot)
	require.NoError(t, err)
	outputs = make(map[string]int)
	for range count {
		got, delivered, err := invokeCommandForTest(reset, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		outputs[string(got.Stdout)]++
	}
	require.Len(t, outputs, count)
	for index := range count {
		require.Equal(t, 1, outputs[fmt.Sprint(index)])
	}
}

func TestCommandAdapterImmutableConfigurationAndOutput(t *testing.T) {
	envelope := commandEnvelopeForTest(`{"args":[],"stdout":"immutable"}`)
	envelope.Fixtures = map[string][]byte{"unused": []byte("fixture")}
	root := t.TempDir()
	adapter, err := NewCommandAdapter(envelope, root, root)
	require.NoError(t, err)
	before, err := adapter.Configuration()
	require.NoError(t, err)
	envelope.Eval[0] = 'X'
	envelope.Responses[0].Data[0] = 'X'
	envelope.Fixtures["unused"][0] = 'X'
	envelope.Name = "changed"
	envelope.Workspace = "changed"
	for range 2 {
		require.NoError(t, adapter.Invoke(context.Background(), nil, root, nil, func(output CommandOutput) error {
			require.Equal(t, "immutable", string(output.Stdout))
			output.Stdout[0] = 'X'
			return nil
		}))
	}
	after, err := adapter.Configuration()
	require.NoError(t, err)
	require.Equal(t, before, after)
	before[0] = 'X'
	again, err := adapter.Configuration()
	require.NoError(t, err)
	require.Equal(t, after, again)
}

func TestCommandAdapterRejectsChangedConfigurationBeforeAllocation(t *testing.T) {
	for _, change := range []string{
		`{"args":[],"sequence":[{"stdout":"changed"}]}`,
		`{"args":[],"sequence":[{"stdout":"original"},{"stdout":"extra"}]}`,
	} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			adapter, err := NewCommandAdapter(commandEnvelopeForTest(`{"args":[],"sequence":[{"stdout":"original"}]}`), root, root)
			require.NoError(t, err)
			_, err = NewCommandAdapter(commandEnvelopeForTest(change), root, root)
			require.ErrorContains(t, err, "configuration changed")
			got, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
			require.NoError(t, err)
			require.True(t, delivered)
			require.Equal(t, "original", string(got.Stdout))
		})
	}
}

func TestCommandAdapterValidatesAllSourcesBeforeBinding(t *testing.T) {
	for _, test := range []struct {
		name     string
		envelope Envelope
		want     string
	}{
		{"late invalid step", commandEnvelopeForTest(`{"args":[],"sequence":[{"stdout":"valid"}]}`, `{"args":["never"],"sequence":[{"stdout":"valid"},{"exit_code":256}]}`), "between 0 and 255"},
		{"late legacy numeric overflow", commandEnvelopeForTest(`{"args":[],"sequence":[{"stdout":"valid"}]}`, `{"args":["never"],"sequence":[{"stdout":1e400}]}`), "decoding command response"},
		{"missing cached unmatched fixture", commandEnvelopeForTest(`{"args":[],"stdout":"valid"}`, `{"args":["never"],"fixture":"absent.txt"}`), "missing from private configuration"},
		{"duplicate raw field", commandEnvelopeForTest(`{"args":[],"sequence":[{"exit_code":0,"exit_code":1}]}`), "duplicate"},
		{"invalid name", func() Envelope {
			envelope := commandEnvelopeForTest(`{"args":[],"stdout":""}`)
			envelope.Name = "../cli"
			return envelope
		}(), "executable name"},
		{"legacy eval", func() Envelope {
			envelope := commandEnvelopeForTest(`{"args":[],"stdout":""}`)
			envelope.Eval = []byte("schemaVersion: \"1.3\"\n")
			return envelope
		}(), "explicit scenario"},
		{"missing scenario", func() Envelope {
			envelope := commandEnvelopeForTest(`{"args":[],"stdout":""}`)
			envelope.Eval = []byte("schemaVersion: \"2.0\"\n")
			return envelope
		}(), "schema"},
		{"wrong adapter kind", func() Envelope {
			envelope := commandEnvelopeForTest(`{"return":{}}`)
			envelope.Kind, envelope.Tool = "mcp", "read"
			return envelope
		}(), "requires command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			_, err := NewCommandAdapter(test.envelope, root, root)
			require.ErrorContains(t, err, test.want)
			files, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Empty(t, files)
		})
	}
}

func TestMaterializeCommandEnvelopeCapturesAllFixturesOnce(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "output.bin"), []byte{0, 1, 2, 255}, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "unmatched.txt"), []byte("unmatched fixture"), 0600))
	envelope := commandEnvelopeForTest(
		`{"args":[],"sequence":[{"fixture":"output.bin"},{"fixture":"output.bin"}]}`,
		`{"args":["never"],"fixture":"unmatched.txt"}`,
	)
	materialized, err := MaterializeCommandEnvelope(envelope, base)
	require.NoError(t, err)
	require.Len(t, materialized.Fixtures, 2)
	require.Equal(t, []byte{0, 1, 2, 255}, materialized.Fixtures["output.bin"])
	require.Equal(t, "unmatched fixture", string(materialized.Fixtures["unmatched.txt"]))
	require.Nil(t, envelope.Fixtures)
	root := t.TempDir()
	adapter, err := NewCommandAdapter(materialized, root, root)
	require.NoError(t, err)
	config, err := adapter.Configuration()
	require.NoError(t, err)
	materialized.Fixtures["output.bin"][0] = 99
	materialized.Responses[0].Data[0] = 'X'
	require.NoError(t, os.Remove(filepath.Join(base, "output.bin")))
	require.NoError(t, os.WriteFile(filepath.Join(base, "unmatched.txt"), []byte("changed"), 0600))

	childEnvelope, err := DecodeEnvelope(config)
	require.NoError(t, err)
	recaptured, err := MaterializeCommandEnvelope(childEnvelope, base)
	require.NoError(t, err)
	require.Equal(t, childEnvelope, recaptured)
	child, err := NewCommandAdapter(childEnvelope, root, root)
	require.NoError(t, err)
	for _, instance := range []*CommandAdapter{adapter, child} {
		got, delivered, err := invokeCommandForTest(instance, context.Background(), nil, nil)
		require.NoError(t, err)
		require.True(t, delivered)
		require.Equal(t, []byte{0, 1, 2, 255}, got.Stdout)
	}
	got, delivered, err := invokeCommandForTest(child, context.Background(), []string{"never"}, nil)
	require.NoError(t, err)
	require.True(t, delivered)
	require.Equal(t, "unmatched fixture", string(got.Stdout))
}

func TestMaterializeCommandEnvelopeValidationBeforeFixtureReads(t *testing.T) {
	base := t.TempDir()
	envelope := commandEnvelopeForTest(
		`{"args":[],"fixture":"absent.txt"}`,
		`{"args":[],"sequence":[{"exit_code":999}]}`,
	)
	_, err := MaterializeCommandEnvelope(envelope, base)
	require.ErrorContains(t, err, "between 0 and 255")
	require.NotContains(t, err.Error(), "no such file")
	envelope.Responses[1].Data = []byte(`{"args":[],"stdout":"valid"}`)
	_, err = MaterializeCommandEnvelope(envelope, base)
	require.ErrorContains(t, err, "capturing command fixture")
	require.ErrorIs(t, err, os.ErrNotExist)

	envelope = commandEnvelopeForTest(`{"args":[],"stdout":"valid"}`, `{"args":[],"fixture":"absent.txt"}`)
	_, err = MaterializeCommandEnvelope(envelope, base)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCommandAdapterConfigurationRoundTripPreservesSource(t *testing.T) {
	envelope := commandEnvelopeForTest(`{"args":[],"sequence":[{"exit_code":0,"stdout":null}]}`)
	envelope.Responses[0] = Fragment{Format: YAML, Data: []byte("args: []\nsequence:\n  - exit_code: 0\n    stdout: null\n")}
	root := t.TempDir()
	adapter, err := NewCommandAdapter(envelope, root, root)
	require.NoError(t, err)
	config, err := adapter.Configuration()
	require.NoError(t, err)
	var decoded Envelope
	require.NoError(t, json.Unmarshal(config, &decoded))
	envelope.Workspace = root
	require.Equal(t, envelope, decoded)
	decoded, err = DecodeEnvelope(config)
	require.NoError(t, err)
	child, err := NewCommandAdapter(decoded, root, root)
	require.NoError(t, err)
	_, delivered, err := invokeCommandForTest(child, context.Background(), nil, nil)
	require.NoError(t, err)
	require.True(t, delivered)
	_, delivered, err = invokeCommandForTest(adapter, context.Background(), nil, nil)
	require.ErrorIs(t, err, faultsequence.ErrExhausted)
	require.False(t, delivered)
}

func TestCommandAdapterFiniteMatchersHaveIndependentState(t *testing.T) {
	adapter := commandAdapterForTest(t,
		`{"args":["one"],"sequence":[{"stdout":"first"}]}`,
		`{"args":["two"],"sequence":[{"stdout":"second"}]}`,
	)
	for index, arg := range []string{"one", "two"} {
		_, delivered, err := invokeCommandForTest(adapter, context.Background(), []string{arg}, func(matcher, step int, transition Transition) error {
			require.Equal(t, index, matcher)
			require.Equal(t, 0, step)
			require.Contains(t, []Transition{Reserved, Delivered}, transition)
			return nil
		})
		require.NoError(t, err)
		require.True(t, delivered)
		_, delivered, err = invokeCommandForTest(adapter, context.Background(), []string{arg}, func(matcher, step int, transition Transition) error {
			require.Equal(t, index, matcher)
			require.Equal(t, -1, step)
			require.Equal(t, Failed, transition)
			return nil
		})
		require.ErrorIs(t, err, faultsequence.ErrExhausted)
		require.False(t, delivered)
	}
}

func TestCommandAdapterPrivateStateErrorsFailClosed(t *testing.T) {
	envelope := commandEnvelopeForTest(`{"args":[],"sequence":[{"stdout":"ready"}]}`)
	base := t.TempDir()
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, nil, 0600))
	for _, root := range []string{"relative-state", filepath.Join(base, "missing"), file} {
		_, err := NewCommandAdapter(envelope, root, base)
		require.Error(t, err)
	}

	envelope.Workspace = base
	namespace, err := bindConfiguration(base, envelope)
	require.NoError(t, err)
	state := filepath.Join(namespace, "response-00000000")
	require.NoError(t, os.WriteFile(state, nil, 0600))
	_, err = NewCommandAdapter(envelope, base, base)
	require.ErrorContains(t, err, "not a directory")
	require.NoError(t, os.Remove(state))
	adapter, err := NewCommandAdapter(envelope, base, base)
	require.NoError(t, err)
	require.NoError(t, os.Remove(state))
	require.NoError(t, os.WriteFile(state, nil, 0600))
	_, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, func(matcher, step int, transition Transition) error {
		require.Equal(t, 0, matcher)
		require.Equal(t, -1, step)
		require.Equal(t, Failed, transition)
		return nil
	})
	require.Error(t, err)
	require.False(t, delivered)
}

func TestCommandAdapterWorkspaceIsImmutableConfiguration(t *testing.T) {
	root, workspace, differentWorkspace := t.TempDir(), t.TempDir(), t.TempDir()
	envelope := commandEnvelopeForTest(`{"args":[],"workdir":"sub","sequence":[{"stdout":"original"},{"stdout":"child"}]}`)
	adapter, err := NewCommandAdapter(envelope, root, filepath.Join(workspace, "sub", ".."))
	require.NoError(t, err)
	require.Equal(t, workspace, adapter.workspace)
	config, err := adapter.Configuration()
	require.NoError(t, err)
	childEnvelope, err := DecodeEnvelope(config)
	require.NoError(t, err)
	require.Equal(t, workspace, childEnvelope.Workspace)

	_, err = NewCommandAdapter(envelope, root, differentWorkspace)
	require.ErrorContains(t, err, "configuration changed")
	_, err = NewCommandAdapter(childEnvelope, root, differentWorkspace)
	require.ErrorContains(t, err, "workspace differs")
	child, err := NewCommandAdapter(childEnvelope, root, "")
	require.NoError(t, err)
	require.Equal(t, workspace, child.workspace)
	normalizedChild, err := NewCommandAdapter(childEnvelope, root, filepath.Join(workspace, "."))
	require.NoError(t, err)
	require.Equal(t, workspace, normalizedChild.workspace)

	require.ErrorIs(t, child.Invoke(context.Background(), nil, filepath.Join(differentWorkspace, "sub"), nil, func(CommandOutput) error {
		t.Fatal("child must not reinterpret workdir against a different workspace")
		return nil
	}), ErrUnmatched)
	for index, instance := range []*CommandAdapter{adapter, child} {
		want := []string{"original", "child"}[index]
		require.NoError(t, instance.Invoke(context.Background(), nil, filepath.Join(workspace, "sub"), func(matcher, step int, transition Transition) error {
			require.Equal(t, 0, matcher)
			require.Equal(t, index, step)
			require.Contains(t, []Transition{Reserved, Delivered}, transition)
			return nil
		}, func(output CommandOutput) error {
			require.Equal(t, want, string(output.Stdout))
			return nil
		}))
	}
}

func TestCommandAdapterUnallocatedFailuresObserveCallerIdentity(t *testing.T) {
	observerFailure := errors.New("native observer failed")
	for _, failure := range []string{"nil callback", "unmatched"} {
		t.Run(failure, func(t *testing.T) {
			adapter := commandAdapterForTest(t, `{"args":[],"sequence":[{"stdout":"first"}]}`)
			observerCalls := 0
			observer := func(matcher, step int, transition Transition) error {
				observerCalls++
				require.Equal(t, -1, matcher)
				require.Equal(t, -1, step)
				require.Equal(t, Failed, transition)
				return observerFailure
			}
			var err error
			if failure == "nil callback" {
				err = adapter.Invoke(context.Background(), nil, adapter.workspace, observer, nil)
				require.ErrorContains(t, err, "delivery callback")
			} else {
				_, delivered, invokeErr := invokeCommandForTest(adapter, context.Background(), []string{"sensitive-argument"}, observer)
				err = invokeErr
				require.False(t, delivered)
				require.ErrorIs(t, err, ErrUnmatched)
				require.NotContains(t, err.Error(), "sensitive-argument")
			}
			require.ErrorIs(t, err, observerFailure)
			require.Equal(t, 1, observerCalls)
			markers, err := os.ReadDir(adapter.entries[0].state)
			require.NoError(t, err)
			require.Empty(t, markers)
			output, delivered, err := invokeCommandForTest(adapter, context.Background(), nil, nil)
			require.NoError(t, err)
			require.True(t, delivered)
			require.Equal(t, "first", string(output.Stdout))
		})
	}
}
