package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var runnerHelperCoverageLine = regexp.MustCompile(`^coverage: (?:(?:0|[1-9][0-9]?)\.[0-9]|100\.0)% of statements$`)

type runnerHelperFraming struct {
	kinds   []string
	next    int
	passed  bool
	covered bool
	pending []byte
}

func newRunnerHelperFraming(kinds ...string) *runnerHelperFraming {
	return &runnerHelperFraming{kinds: kinds}
}

func (f *runnerHelperFraming) write(data []byte) ([][]byte, error) {
	f.pending = append(f.pending, data...)
	var records [][]byte
	for {
		index := bytes.IndexByte(f.pending, '\n')
		if index < 0 {
			return records, nil
		}
		line := bytes.TrimSuffix(f.pending[:index], []byte{'\r'})
		f.pending = f.pending[index+1:]
		if bytes.Equal(line, []byte("PASS")) {
			if f.passed || f.next != len(f.kinds) {
				return nil, errors.New("helper PASS is duplicate or precedes required records")
			}
			f.passed = true
			continue
		}
		if runnerHelperCoverageLine.Match(line) || bytes.Equal(line, []byte("coverage: [no statements]")) {
			if !f.passed || f.covered {
				return nil, errors.New("helper coverage is duplicate or precedes PASS")
			}
			f.covered = true
			continue
		}
		if f.passed {
			return nil, errors.New("unexpected helper output after PASS")
		}
		var record struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decoding helper record: %w", err)
		}
		if f.next == len(f.kinds) || record.Kind != f.kinds[f.next] {
			return nil, fmt.Errorf("unexpected or duplicate helper record %q at position %d", record.Kind, f.next)
		}
		f.next++
		records = append(records, bytes.Clone(line))
	}
}

func (f *runnerHelperFraming) finish() error {
	if len(f.pending) != 0 {
		return errors.New("helper output ends with an incomplete line")
	}
	if f.next != len(f.kinds) || !f.passed {
		return errors.New("helper output lacks required records or PASS")
	}
	return nil
}

func decodeRunnerHelperSnapshot(output []byte, kinds ...string) ([][]byte, error) {
	framing := newRunnerHelperFraming(kinds...)
	records, err := framing.write(output)
	if err != nil {
		return nil, err
	}
	if err := framing.finish(); err != nil {
		return nil, err
	}
	return records, nil
}

func TestRunnerTerminalHelperProtocol(t *testing.T) {
	capability := `{"kind":"capability","no_deadline":true,"go_version":"go1.26.10"}`
	normal := capability + "\nPASS\n"
	tests := []struct {
		name    string
		output  string
		valid   bool
		covered bool
	}{
		{name: "normal", output: normal, valid: true},
		{name: "observed-coverage", output: normal + "coverage: 0.0% of statements\n", valid: true, covered: true},
		{name: "full-coverage", output: normal + "coverage: 100.0% of statements\n", valid: true, covered: true},
		{name: "fractional-coverage", output: normal + "coverage: 7.3% of statements\n", valid: true, covered: true},
		{name: "no-statements", output: normal + "coverage: [no statements]\n", valid: true, covered: true},
		{name: "crlf", output: strings.ReplaceAll(normal+"coverage: 0.0% of statements\n", "\n", "\r\n"), valid: true, covered: true},
		{name: "missing-record", output: "PASS\n"},
		{name: "wrong-record", output: "{\"kind\":\"ready\"}\nPASS\n"},
		{name: "duplicate-record", output: capability + "\n" + normal},
		{name: "malformed-record", output: "{\"kind\":\nPASS\n"},
		{name: "nonobject-record", output: "null\nPASS\n"},
		{name: "trailing-record-value", output: capability + "{}\nPASS\n"},
		{name: "missing-pass", output: capability + "\n"},
		{name: "duplicate-pass", output: normal + "PASS\n"},
		{name: "pass-before-record", output: "PASS\n" + capability + "\n"},
		{name: "coverage-before-pass", output: capability + "\ncoverage: 0.0% of statements\nPASS\n"},
		{name: "duplicate-coverage", output: normal + strings.Repeat("coverage: 0.0% of statements\n", 2)},
		{name: "malformed-coverage", output: normal + "coverage: 0.0% of statements extra\n"},
		{name: "coverage-over-limit", output: normal + "coverage: 100.1% of statements\n"},
		{name: "negative-coverage", output: normal + "coverage: -0.1% of statements\n"},
		{name: "nonfinite-coverage", output: normal + "coverage: NaN% of statements\n"},
		{name: "coverage-extra-precision", output: normal + "coverage: 0.00% of statements\n"},
		{name: "record-after-pass", output: normal + capability + "\n"},
		{name: "failure", output: capability + "\nFAIL\n"},
		{name: "unknown", output: normal + "unexpected diagnostic\n"},
		{name: "blank", output: normal + "\n"},
		{name: "incomplete-record", output: capability},
		{name: "incomplete-pass", output: strings.TrimSuffix(normal, "\n")},
		{name: "incomplete-coverage", output: normal + "coverage: 0.0% of statements"},
		{name: "incomplete-extra", output: normal + "extra"},
	}
	for _, consumer := range []string{"snapshot", "stream"} {
		t.Run(consumer, func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					if consumer == "snapshot" {
						records, err := decodeRunnerHelperSnapshot([]byte(test.output), "capability")
						if !test.valid {
							require.Error(t, err)
							return
						}
						require.NoError(t, err)
						require.Equal(t, [][]byte{[]byte(capability)}, records)
						return
					}
					for _, chunkSize := range []int{1, 7, len(test.output)} {
						framing := newRunnerHelperFraming("capability")
						var records [][]byte
						var err error
						for start := 0; start < len(test.output) && err == nil; start += chunkSize {
							var batch [][]byte
							batch, err = framing.write([]byte(test.output[start:min(start+chunkSize, len(test.output))]))
							records = append(records, batch...)
						}
						err = errors.Join(err, framing.finish())
						if !test.valid {
							require.Error(t, err, "chunk size %d", chunkSize)
							continue
						}
						require.NoError(t, err, "chunk size %d", chunkSize)
						require.Equal(t, [][]byte{[]byte(capability)}, records)
						require.Equal(t, test.covered, framing.covered)
					}
				})
			}
		})
	}
	t.Run("controller-order", func(t *testing.T) {
		kinds := []string{"ready", "armed", "capability", "started", "consumed", "result", "finished"}
		var output strings.Builder
		for _, kind := range kinds {
			fmt.Fprintf(&output, "{\"kind\":%q}\n", kind)
		}
		output.WriteString("PASS\ncoverage: 0.0% of statements\n")
		records, err := decodeRunnerHelperSnapshot([]byte(output.String()), kinds...)
		require.NoError(t, err)
		require.Len(t, records, len(kinds))
		framing := newRunnerHelperFraming(kinds...)
		var streamed [][]byte
		for _, value := range []byte(output.String()) {
			batch, err := framing.write([]byte{value})
			require.NoError(t, err)
			streamed = append(streamed, batch...)
		}
		require.NoError(t, framing.finish())
		require.Equal(t, records, streamed)
		for _, invalid := range []string{
			strings.Replace(output.String(), `"armed"`, `"ready"`, 1),
			strings.Replace(output.String(), `"result"`, `"finished"`, 1),
			strings.Replace(output.String(), "\"kind\":\"consumed\"", "\"kind\":\"started\"", 1),
			strings.Replace(output.String(), "{\"kind\":\"consumed\"}\n", "", 1),
			strings.Replace(output.String(), "{\"kind\":\"finished\"}\n", "", 1),
		} {
			_, err := decodeRunnerHelperSnapshot([]byte(invalid), kinds...)
			require.Error(t, err)
		}
	})
}

const runnerHelperFixtureEnv = "WAZA_RUNNER_HELPER_PROTOCOL_FIXTURE"
const runnerHelperFixtureRecord = "{\"kind\":\"portable-fixture\",\"source\":\"nonnative\"}\n"

type runnerHelperFixtureOutput struct {
	sync.Mutex
	data []byte
}

func (w *runnerHelperFixtureOutput) Write(data []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	if len(w.data)+len(data) > 1<<20 {
		return 0, errors.New("portable helper output exceeded 1 MiB")
	}
	w.data = append(w.data, data...)
	return len(data), nil
}

func (w *runnerHelperFixtureOutput) snapshot() []byte {
	w.Lock()
	defer w.Unlock()
	return bytes.Clone(w.data)
}

func TestRunnerTerminalHelperProtocolOutputBound(t *testing.T) {
	var output runnerHelperFixtureOutput
	payload := bytes.Repeat([]byte{'x'}, 1<<20)
	n, err := output.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	n, err = output.Write([]byte{'x'})
	require.ErrorContains(t, err, "exceeded 1 MiB")
	require.Zero(t, n)
	require.Equal(t, payload, output.snapshot())
	snapshot := output.snapshot()
	snapshot[0] = 'y'
	require.Equal(t, payload, output.snapshot(), "snapshot must not expose mutable capture storage")
}

func runRunnerHelperFixture(t *testing.T, mode string) ([]byte, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerTerminalHelperProtocolFixture$")
	cmd.Env = append(os.Environ(), runnerHelperFixtureEnv+"="+mode, "GOCOVERDIR="+t.TempDir())
	var stdout, stderr runnerHelperFixtureOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	require.NoError(t, ctx.Err(), "portable helper required safety cancellation")
	return stdout.snapshot(), stderr.snapshot(), err
}

func TestRunnerTerminalHelperProtocolReexec(t *testing.T) {
	stdout, stderr, err := runRunnerHelperFixture(t, "success")
	require.NoError(t, err, "stdout=%s stderr=%s", stdout, stderr)
	require.Empty(t, stderr)
	require.Contains(t, string(stdout), runnerHelperFixtureRecord+"PASS\n")
	covered := testing.CoverMode() != ""
	require.Equal(t, covered, bytes.Contains(stdout, []byte("\ncoverage: ")),
		"covered invocation must observe the real child testing trailer")
	t.Logf("nonnative fixture returned through TestMain: actual coverage trailer=%t; mode=%q", covered, testing.CoverMode())
	t.Run("snapshot", func(t *testing.T) {
		records, err := decodeRunnerHelperSnapshot(stdout, "portable-fixture")
		require.NoError(t, err)
		require.Equal(t, [][]byte{bytes.TrimSuffix([]byte(runnerHelperFixtureRecord), []byte{'\n'})}, records)
	})
	t.Run("stream", func(t *testing.T) {
		framing := newRunnerHelperFraming("portable-fixture")
		var records [][]byte
		for _, value := range stdout {
			batch, err := framing.write([]byte{value})
			require.NoError(t, err)
			records = append(records, batch...)
		}
		require.NoError(t, framing.finish())
		require.Equal(t, [][]byte{bytes.TrimSuffix([]byte(runnerHelperFixtureRecord), []byte{'\n'})}, records)
		require.Equal(t, covered, framing.covered)
	})
}

func TestRunnerTerminalHelperProtocolNonzeroExit(t *testing.T) {
	stdout, stderr, err := runRunnerHelperFixture(t, "failure")
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.NotZero(t, exit.ExitCode())
	require.Empty(t, stderr)
	require.Contains(t, string(stdout), "intentional nonnative fixture failure")
	_, err = decodeRunnerHelperSnapshot(stdout, "portable-fixture")
	require.Error(t, err, "a helper test failure must not become a successful record")
	framing := newRunnerHelperFraming("portable-fixture")
	_, err = framing.write(stdout)
	require.Error(t, errors.Join(err, framing.finish()))
}

func TestRunnerTerminalHelperProtocolFixture(t *testing.T) {
	mode := os.Getenv(runnerHelperFixtureEnv)
	if mode == "" {
		return
	}
	require.Contains(t, []string{"success", "failure"}, mode)
	_, err := io.WriteString(os.Stdout, runnerHelperFixtureRecord)
	require.NoError(t, err)
	if mode == "failure" {
		t.Error("intentional nonnative fixture failure")
	}
}
