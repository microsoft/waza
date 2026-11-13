package runneradapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// MaxReplyBytes bounds one public reply read by a reference contract.
const MaxReplyBytes = 32 << 20

// ProcessContract invokes the public waza CLI once per call. StoreArgs (for
// example --store DIR) are forwarded verbatim; the adapter never chooses them.
type ProcessContract struct {
	Executable string
	StoreArgs  []string
	Stderr     io.Writer
}

func (p ProcessContract) Call(ctx context.Context, call Call) (Reply, error) {
	if p.Executable == "" {
		return Reply{}, errors.New("process contract requires the waza executable path")
	}
	args := append(slices.Clone(call.Command), p.StoreArgs...)
	command := exec.CommandContext(ctx, p.Executable, args...) //nolint:gosec // executable and argv come from the host and the fixed public command table
	command.Stdin = strings.NewReader(call.Input)
	var stdout boundedBuffer
	command.Stdout = &stdout
	command.Stderr = p.Stderr
	if command.Stderr == nil {
		command.Stderr = io.Discard
	}
	err := command.Run()
	if stdout.exceeded {
		return Reply{}, fmt.Errorf("public reply exceeds %d bytes", MaxReplyBytes)
	}
	if err == nil {
		return Reply{ExitCode: 0, Body: stdout.Bytes()}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return Reply{ExitCode: exitErr.ExitCode(), Body: stdout.Bytes()}, nil
	}
	return Reply{}, fmt.Errorf("running public command: %w", err)
}

type boundedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > MaxReplyBytes {
		b.exceeded = true
		return 0, io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

// HTTPContract invokes the public dashboard HTTP API once per call.
type HTTPContract struct {
	BaseURL string
	Client  *http.Client
}

func (h HTTPContract) Call(ctx context.Context, call Call) (Reply, error) {
	client := h.Client
	if client == nil {
		client = http.DefaultClient
	}
	var body io.Reader
	if call.HTTP.Method == http.MethodPost {
		body = strings.NewReader(call.Input)
	}
	request, err := http.NewRequestWithContext(ctx, call.HTTP.Method, strings.TrimRight(h.BaseURL, "/")+call.HTTP.Path, body)
	if err != nil {
		return Reply{}, fmt.Errorf("building public request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return Reply{}, fmt.Errorf("calling public route: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxReplyBytes+1))
	if err != nil {
		return Reply{}, fmt.Errorf("reading public reply: %w", err)
	}
	if len(data) > MaxReplyBytes {
		return Reply{}, fmt.Errorf("public reply exceeds %d bytes", MaxReplyBytes)
	}
	if header := response.Header.Get("X-Waza-Exit-Code"); header != "" {
		code, err := strconv.Atoi(header)
		if err != nil || code < 0 || code > 6 {
			return Reply{}, fmt.Errorf("public route returned invalid exit code header %q", header)
		}
		return Reply{ExitCode: code, Body: data}, nil
	}
	if response.StatusCode != http.StatusOK {
		return Reply{}, fmt.Errorf("public route returned HTTP %d without a contract result", response.StatusCode)
	}
	return Reply{ExitCode: 0, Body: data}, nil
}
