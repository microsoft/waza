// Package runneridentity derives logical identities from declared runner input.
// It performs no execution, filesystem access, or runtime attestation.
package runneridentity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/runnercontract"
)

// Version is independent of request and result schema versions. Changing any
// canonicalization, field, hierarchy, or framing rule requires a new version.
const Version = "sha256-waza-c14n-v1"

type Identity struct {
	Version         string `json:"version"`
	CanonicalDigest string `json:"canonical_digest"`
	Run             string `json:"run"`
	Shard           string `json:"shard"`
	Task            string `json:"task"`
	Trial           string `json:"trial"`
	Attempt         string `json:"attempt"`
}

// DeriveJSON validates the original request wire shape before deriving identities.
// Captured sources are canonicalized directly, never decoded through model types.
func DeriveJSON(data []byte) (*Identity, error) {
	request, err := runnercontract.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("identity request: %w", err)
	}
	return derive(request)
}

// Derive validates the current typed request shape. Callers retaining original
// wire input should use DeriveJSON to reject missing fields and ambiguous JSON.
// Semantic execution admission (including task ID and bounds) is still required.
func Derive(request *runnercontract.Request) (*Identity, error) {
	if request == nil {
		return nil, errors.New("identity request is nil")
	}
	if !utf8.ValidString(request.Input.Eval) || !utf8.ValidString(request.Input.Task) {
		return nil, errors.New("identity source is not UTF-8")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encoding identity request: %w", err)
	}
	return DeriveJSON(data)
}

func derive(request *runnercontract.Request) (*Identity, error) {
	if request.Input.Shard != request.Selection.Shard {
		return nil, errors.New("identity selection shard does not match declared input shard")
	}
	eval, err := CanonicalSource(request.Input.Eval)
	if err != nil {
		return nil, fmt.Errorf("identity eval: %w", err)
	}
	task, err := CanonicalSource(request.Input.Task)
	if err != nil {
		return nil, fmt.Errorf("identity task: %w", err)
	}
	run := digest("run", request.SchemaVersion, request.RunKey, strconv.Itoa(request.TimeoutMS), string(eval))
	shard := digest("shard", run, request.Input.Shard)
	taskID := digest("task", shard, request.Selection.Task, string(task))
	trial := digest("trial", taskID, strconv.Itoa(request.Selection.Trial))
	attempt := digest("attempt", trial, strconv.Itoa(request.Selection.Attempt))
	return &Identity{
		Version: Version, CanonicalDigest: digest("input", run, shard, taskID, trial, attempt),
		Run: "run-" + run, Shard: "shard-" + shard, Task: "task-" + taskID,
		Trial: "trial-" + trial, Attempt: "attempt-" + attempt,
	}, nil
}

func digest(namespace string, fields ...string) string {
	var payload bytes.Buffer
	payload.WriteString(Version)
	payload.WriteByte(0)
	payload.WriteString(namespace)
	payload.WriteByte(0)
	count(&payload, 'a', len(fields))
	for _, field := range fields {
		frame(&payload, 's', field)
	}
	sum := sha256.Sum256(payload.Bytes())
	return hex.EncodeToString(sum[:])
}
