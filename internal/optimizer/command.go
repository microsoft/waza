package optimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxAdapterOutputBytes = 8 << 20
	maxAdapterStderrBytes = 4 << 10
	defaultAdapterTimeout = 10 * time.Minute
	adapterWaitDelay      = 2 * time.Second
)

// baseAdapterEnv is passed to command adapters; everything else, including
// provider credentials, must be named explicitly with --adapter-env.
var baseAdapterEnv = []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL"}

// CommandAdapter runs an explicitly configured external process once per
// call: one JSON request on stdin, one JSON response on stdout. No shell is
// used and no code is loaded into Waza.
type CommandAdapter struct {
	Path    string
	Args    []string
	EnvKeys []string
	Timeout time.Duration
}

// NewCommandAdapter resolves command to an absolute executable path. Because
// each call runs in a fresh temporary directory, arguments naming existing
// relative paths (for example a script) are made absolute.
func NewCommandAdapter(command string, args, envKeys []string, timeout time.Duration) (*CommandAdapter, error) {
	if command == "" {
		return nil, newError(CategoryInvalidConfig, "--adapter-command is required for the command adapter")
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return nil, wrapError(CategoryInvalidConfig, err, "resolving adapter command %q", command)
	}
	if abs, err := filepath.Abs(resolved); err == nil {
		resolved = abs
	}
	if timeout <= 0 {
		timeout = defaultAdapterTimeout
	}
	resolvedArgs := make([]string, len(args))
	for i, arg := range args {
		resolvedArgs[i] = arg
		if arg == "" || filepath.IsAbs(arg) || strings.HasPrefix(arg, "-") {
			continue
		}
		if _, err := os.Stat(arg); err == nil {
			if abs, err := filepath.Abs(arg); err == nil {
				resolvedArgs[i] = abs
			}
		}
	}
	return &CommandAdapter{Path: resolved, Args: resolvedArgs, EnvKeys: envKeys, Timeout: timeout}, nil
}

type describeRequest struct {
	Kind            string             `json:"kind"`
	ContractVersion string             `json:"contract_version"`
	Operation       string             `json:"operation"`
	Harness         CapabilityDocument `json:"harness"`
}

type describeResponse struct {
	Kind            string      `json:"kind"`
	ContractVersion string      `json:"contract_version"`
	Adapter         AdapterInfo `json:"adapter"`
}

func (c *CommandAdapter) Describe(ctx context.Context) (AdapterInfo, error) {
	var resp describeResponse
	req := describeRequest{Kind: AdapterRequestKind, ContractVersion: ContractVersion, Operation: OperationDescribe, Harness: Capabilities()}
	if err := c.call(ctx, OperationDescribe, req, &resp); err != nil {
		return AdapterInfo{}, err
	}
	if resp.Kind != AdapterResponseKind {
		return AdapterInfo{}, newError(CategoryMalformedProposal, "describe response kind %q, want %q", resp.Kind, AdapterResponseKind)
	}
	return resp.Adapter, nil
}

func (c *CommandAdapter) Propose(ctx context.Context, req ProposeRequest) (ProposeResponse, error) {
	var resp ProposeResponse
	if err := c.call(ctx, OperationPropose, req, &resp); err != nil {
		return ProposeResponse{}, err
	}
	return resp, nil
}

func (c *CommandAdapter) env() []string {
	env := []string{}
	for _, key := range append(append([]string{}, baseAdapterEnv...), c.EnvKeys...) {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func (c *CommandAdapter) call(parent context.Context, operation string, req, resp any) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()
	workdir, err := os.MkdirTemp("", "waza-optimizer-adapter-*")
	if err != nil {
		return wrapError(CategoryInfrastructureFailure, err, "creating adapter work directory")
	}
	defer func() { _ = os.RemoveAll(workdir) }()

	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Dir = workdir
	cmd.Env = c.env()
	cmd.Stdin = bytes.NewReader(payload)
	stdout := &limitedBuffer{limit: maxAdapterOutputBytes}
	stderr := &tailBuffer{limit: maxAdapterStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = adapterWaitDelay
	configureAdapterProcess(cmd)
	runErr := cmd.Run()
	if parent.Err() != nil {
		return context.Cause(parent)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return newError(CategoryOptimizerFailure, "adapter command exceeded --adapter-timeout %s", c.Timeout)
	}
	if stdout.overflow {
		return newError(CategoryOptimizerFailure, "adapter stdout exceeded %d bytes", maxAdapterOutputBytes)
	}
	if runErr != nil {
		return newError(CategoryOptimizerFailure, "adapter command failed: %v; stderr: %s", runErr, stderr.String())
	}
	if err := validateAdapterResponse(operation, stdout.Bytes()); err != nil {
		return err
	}
	if err := decodeStrict(stdout.Bytes(), resp); err != nil {
		return wrapError(CategoryMalformedProposal, err, "decoding adapter response")
	}
	return nil
}

var adapterResponseSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	const url = "https://microsoft.github.io/waza/schemas/optimizer-adapter-response-1.0.schema.json"
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.OptimizerAdapterResponseSchemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
})

// validateAdapterResponse checks a raw reply against the bundled response
// schema for the operation, so missing or invalid values are never decoded
// into zero values. An unsupported envelope version is reported as such.
func validateAdapterResponse(operation string, data []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return wrapError(CategoryMalformedProposal, err, "decoding adapter response")
	}
	var version string
	if raw, ok := envelope["contract_version"]; ok {
		if err := json.Unmarshal(raw, &version); err != nil {
			return newError(CategoryMalformedProposal, "adapter response contract_version must be a string")
		}
		if version != ContractVersion {
			return newError(CategoryUnsupportedVersion, "adapter response contract_version %q is not supported (want %q)", version, ContractVersion)
		}
	}
	_, hasAdapter := envelope["adapter"]
	if (operation == OperationDescribe) != hasAdapter {
		return newError(CategoryMalformedProposal, "adapter response does not match the %s operation", operation)
	}
	sch, err := adapterResponseSchema()
	if err != nil {
		return wrapError(CategoryInfrastructureFailure, err, "compiling adapter response schema")
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return wrapError(CategoryMalformedProposal, err, "decoding adapter response")
	}
	if err := sch.Validate(inst); err != nil {
		return wrapError(CategoryMalformedProposal, err, "adapter response does not match the contract schema")
	}
	return nil
}

// limitedBuffer records overflow instead of silently truncating. It does not
// embed bytes.Buffer so io.Copy cannot bypass the limit through ReadFrom.
type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, fmt.Errorf("output exceeds %d bytes", b.limit)
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }

// tailBuffer keeps the last limit bytes for diagnostics.
type tailBuffer struct {
	data  []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = b.data[len(b.data)-b.limit:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(bytes.TrimSpace(b.data)) }
