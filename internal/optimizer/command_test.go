package optimizer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/microsoft/waza/schemas"
	"github.com/stretchr/testify/require"
)

const (
	helperModeEnv    = "WAZA_OPTIMIZER_HELPER_MODE"
	helperPIDFileEnv = "WAZA_OPTIMIZER_HELPER_PID_FILE"
)

// TestHelperAdapterProcess is not a real test: it is re-executed as an
// external command adapter by the tests below.
func TestHelperAdapterProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("helper process")
	}
	data, _ := io.ReadAll(os.Stdin)
	var req map[string]any
	_ = json.Unmarshal(data, &req)
	write := func(v any) {
		out, _ := json.Marshal(v)
		_, _ = os.Stdout.Write(out)
		os.Exit(0)
	}
	switch mode {
	case "bad-json":
		fmt.Print("{not json")
		os.Exit(0)
	case "oversize":
		chunk := strings.Repeat("x", 1<<20)
		for range 10 {
			fmt.Print(chunk)
		}
		os.Exit(0)
	case "exit":
		fmt.Fprint(os.Stderr, "adapter crashed")
		os.Exit(3)
	case "hang":
		time.Sleep(time.Minute)
	case "spawn":
		child := exec.Command(os.Args[0], "-test.run=^TestHelperAdapterProcess$")
		child.Env = append(os.Environ(), helperModeEnv+"=hang")
		if child.Start() != nil {
			os.Exit(4)
		}
		_ = os.WriteFile(os.Getenv(helperPIDFileEnv), []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		time.Sleep(time.Minute)
	case "env":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true,
			"usage": map[string]any{"input_tokens": len(os.Getenv("WAZA_SECRET_PROBE")), "output_tokens": len(os.Getenv(usagetelemetry.OptOutEnv)), "premium_requests": 0}})
	case "telemetry":
		cert, err := base64.StdEncoding.DecodeString(os.Getenv("TEST_COLLECTOR_CERT"))
		if err != nil {
			os.Exit(5)
		}
		parsed, err := x509.ParseCertificate(cert)
		if err != nil {
			os.Exit(5)
		}
		roots := x509.NewCertPool()
		roots.AddCert(parsed)
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
		restore := usagetelemetry.OverrideActivationForTesting(os.Getenv("TEST_COLLECTOR_URL"), client, true, os.Getenv("TEST_CI_ON") == "1")
		defer restore()
		p := usagetelemetry.Resolve(usagetelemetry.Inputs{SettingsPath: os.Getenv("TEST_SETTINGS"), WorkDir: "."})
		usagetelemetry.Propagate(p)
		usagetelemetry.Start(p, "dev")
		usagetelemetry.RecordCommand("run", usagetelemetry.CommandSuccess, time.Second)
		usagetelemetry.RecordExecution(usagetelemetry.SurfaceBenchmark, usagetelemetry.ModeMock, usagetelemetry.ExecutionPassed, 1, time.Second)
		usagetelemetry.Shutdown(time.Second * 5)
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true,
			"usage": map[string]any{"input_tokens": len(os.Getenv("GITHUB_ACTIONS")), "output_tokens": len(os.Getenv(usagetelemetry.CIEnv)), "premium_requests": 0}})
	case "null-content":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion,
			"candidate": map[string]any{"parent_id": "sha256:" + strings.Repeat("a", 64), "files": []any{map[string]any{"path": "g.md", "content": nil}}}})
	case "empty-usage":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true, "usage": map[string]any{}})
	case "negative-usage":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true,
			"usage": map[string]any{"input_tokens": -1, "output_tokens": 0, "premium_requests": 0}})
	case "future-version":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": "9.0", "done": true})
	case "describe-as-propose":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "adapter": DefaultFakeInfo()})
	case "unknown-field":
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true, "extra": 1})
	case "plus":
		if req["operation"] == OperationDescribe {
			info := DefaultFakeInfo()
			info.Name = "helper"
			write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "adapter": info})
		}
		parent, _ := req["parent"].(map[string]any)
		files, _ := parent["files"].([]any)
		f, _ := files[0].(map[string]any)
		content, _ := f["content"].(string)
		if strings.Count(content, "+") >= 2 {
			write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion, "done": true})
		}
		write(map[string]any{"kind": AdapterResponseKind, "contract_version": ContractVersion,
			"candidate": map[string]any{"parent_id": parent["id"], "files": []any{map[string]any{"path": f["path"], "content": content + "+"}}}})
	}
	os.Exit(2)
}

func helperAdapter(t *testing.T, mode string, timeout time.Duration, env ...string) *CommandAdapter {
	t.Helper()
	t.Setenv(helperModeEnv, mode)
	a, err := NewCommandAdapter(os.Args[0], []string{"-test.run=^TestHelperAdapterProcess$"}, append([]string{helperModeEnv}, env...), timeout)
	require.NoError(t, err)
	return a
}

func proposeReq() ProposeRequest {
	return ProposeRequest{Kind: AdapterRequestKind, ContractVersion: ContractVersion, Operation: OperationPropose, Iteration: 1,
		Target: TargetContextOverlay, Parent: ParentCandidate{ID: "sha256:" + strings.Repeat("a", 64), Files: []FileContent{{Path: "g.md", Content: "x"}}}}
}

func TestCommandAdapterFailures(t *testing.T) {
	cases := map[string]string{
		"bad-json":      CategoryMalformedProposal,
		"unknown-field": CategoryMalformedProposal,
		"oversize":      CategoryOptimizerFailure,
		"exit":          CategoryOptimizerFailure,
		// Schema-invalid replies must not decode into empty files or zero spend.
		"null-content":        CategoryMalformedProposal,
		"empty-usage":         CategoryMalformedProposal,
		"negative-usage":      CategoryMalformedProposal,
		"describe-as-propose": CategoryMalformedProposal,
		"future-version":      CategoryUnsupportedVersion,
	}
	for mode, category := range cases {
		t.Run(mode, func(t *testing.T) {
			_, err := helperAdapter(t, mode, time.Minute).Propose(context.Background(), proposeReq())
			require.Equal(t, category, CategoryOf(err), err)
		})
	}
	t.Run("timeout", func(t *testing.T) {
		start := time.Now()
		_, err := helperAdapter(t, "hang", 200*time.Millisecond).Propose(context.Background(), proposeReq())
		require.Equal(t, CategoryOptimizerFailure, CategoryOf(err), err)
		require.Contains(t, err.Error(), "adapter-timeout")
		require.Less(t, time.Since(start), 20*time.Second)
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		_, err := helperAdapter(t, "hang", time.Minute).Propose(ctx, proposeReq())
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("relative script args are made absolute", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		require.NoError(t, os.WriteFile("adapter.py", nil, 0o600))
		a, err := NewCommandAdapter(os.Args[0], []string{"adapter.py", "-flag", "not-a-file"}, nil, 0)
		require.NoError(t, err)
		abs, err := filepath.Abs("adapter.py")
		require.NoError(t, err)
		require.Equal(t, []string{abs, "-flag", "not-a-file"}, a.Args)
	})
	t.Run("missing command", func(t *testing.T) {
		_, err := NewCommandAdapter("waza-no-such-adapter-binary", nil, nil, 0)
		require.Equal(t, CategoryInvalidConfig, CategoryOf(err))
	})
}

func TestCommandAdapterEnvironmentAllowlist(t *testing.T) {
	t.Setenv("WAZA_SECRET_PROBE", "secret")
	resp, err := helperAdapter(t, "env", time.Minute).Propose(context.Background(), proposeReq())
	require.NoError(t, err)
	require.Equal(t, 0, resp.Usage.InputTokens, "unlisted variables are not passed")
	resp, err = helperAdapter(t, "env", time.Minute, "WAZA_SECRET_PROBE").Propose(context.Background(), proposeReq())
	require.NoError(t, err)
	require.Equal(t, len("secret"), resp.Usage.InputTokens, "--adapter-env passes named variables")
}

func TestCommandAdapterPreservesUsageTelemetryDenial(t *testing.T) {
	t.Setenv(usagetelemetry.OptOutEnv, "1")
	resp, err := helperAdapter(t, "env", time.Minute).Propose(context.Background(), proposeReq())
	require.NoError(t, err)
	require.Equal(t, 1, resp.Usage.OutputTokens, "adapter children inherit the telemetry opt-out")
}

func TestCommandAdapterPreservesCIPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ciOn       bool
		denial     string
		wantEvents int
	}{
		{name: "CI disabled"},
		{name: "CI enabled", ciOn: true, wantEvents: 1},
		{name: "CI enabled with opt out", ciOn: true, denial: "env"},
		{name: "CI enabled with host denial", ciOn: true, denial: "host"},
		{name: "CI enabled with project denial", ciOn: true, denial: "project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []map[string]any
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var event map[string]any
				if err := json.NewDecoder(r.Body).Decode(&event); err == nil {
					mu.Lock()
					events = append(events, event)
					mu.Unlock()
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			t.Cleanup(server.Close)
			t.Setenv("GITHUB_ACTIONS", "synthetic-ci-value")
			t.Setenv(usagetelemetry.CIEnv, "")
			t.Setenv(usagetelemetry.OptOutEnv, "")
			t.Setenv(usagetelemetry.NestedEnv, "")
			t.Setenv("DO_NOT_TRACK", "")
			t.Setenv("AZURE_DEV_COLLECT_TELEMETRY", "")
			path := filepath.Join(t.TempDir(), "telemetry.json")
			dir := t.TempDir()
			if tc.denial == "project" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, ".waza.yaml"), []byte("telemetry:\n  disabled: true\n"), 0o600))
			}
			if tc.denial == "env" {
				t.Setenv(usagetelemetry.OptOutEnv, "1")
			}
			if tc.denial == "host" {
				t.Setenv("DO_NOT_TRACK", "1")
			}
			restore := usagetelemetry.OverrideActivationForTesting(server.URL, server.Client(), true, tc.ciOn)
			defer restore()
			p := usagetelemetry.Resolve(usagetelemetry.Inputs{SettingsPath: path, WorkDir: dir})
			usagetelemetry.Propagate(p)
			t.Setenv("TEST_COLLECTOR_CERT", base64.StdEncoding.EncodeToString(server.Certificate().Raw))
			t.Setenv("TEST_COLLECTOR_URL", server.URL)
			t.Setenv("TEST_SETTINGS", path)
			ciOn := "0"
			if tc.ciOn {
				ciOn = "1"
			}
			t.Setenv("TEST_CI_ON", ciOn)
			resp, err := helperAdapter(t, "telemetry", time.Minute, "TEST_COLLECTOR_CERT", "TEST_COLLECTOR_URL", "TEST_SETTINGS", "TEST_CI_ON").Propose(t.Context(), proposeReq())
			require.NoError(t, err)
			require.Equal(t, 0, resp.Usage.InputTokens, "raw CI values must not cross the adapter")
			require.Equal(t, 1, resp.Usage.OutputTokens, "CI marker is sanitized and inherited")
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, events, tc.wantEvents)
			for _, event := range events {
				require.Equal(t, true, event["ci"])
				require.Equal(t, "execution_completed", event["event"])
				require.NotContains(t, event, "installation_id")
			}
			require.NoFileExists(t, path)
		})
	}
}

func TestCommandAdapterResponsesMatchSchema(t *testing.T) {
	a := helperAdapter(t, "plus", time.Minute)
	info, err := a.Describe(context.Background())
	require.NoError(t, err)
	require.Equal(t, "helper", info.Name)
	resp, err := a.Propose(context.Background(), proposeReq())
	require.NoError(t, err)
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	validateAgainst(t, "optimizer-adapter-response-1.0.schema.json", schemas.OptimizerAdapterResponseSchemaJSON, data)
	data, err = json.Marshal(describeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion, Adapter: info})
	require.NoError(t, err)
	validateAgainst(t, "optimizer-adapter-response-1.0.schema.json", schemas.OptimizerAdapterResponseSchemaJSON, data)
	data, err = json.Marshal(describeRequest{Kind: AdapterRequestKind, ContractVersion: ContractVersion, Operation: OperationDescribe, Harness: Capabilities()})
	require.NoError(t, err)
	validateAgainst(t, "optimizer-adapter-request-1.0.schema.json", schemas.OptimizerAdapterRequestSchemaJSON, data)
}

func TestCommandAdapterDescribeRejectsUnsupportedVersion(t *testing.T) {
	_, err := helperAdapter(t, "future-version", time.Minute).Describe(context.Background())
	require.Equal(t, CategoryUnsupportedVersion, CategoryOf(err), err)
}
