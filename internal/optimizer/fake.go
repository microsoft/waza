package optimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
)

// FakeProposal is one scripted step for FakeAdapter.
type FakeProposal struct {
	// Files replaces or adds parent files by path.
	Files     map[string]string `json:"files,omitempty"`
	Rationale string            `json:"rationale,omitempty"`
	Done      bool              `json:"done,omitempty"`
	// Error makes Propose fail with an optimizer error.
	Error string `json:"error,omitempty"`
	// Hang blocks until the context ends (cancellation/timeout tests).
	Hang bool `json:"hang,omitempty"`
	// Raw is returned verbatim, bypassing parent handling (malformed tests).
	Raw   *ProposeResponse `json:"-"`
	Usage *Usage           `json:"usage,omitempty"`
}

// FakeScript is the JSON form accepted by `--fake-script`.
type FakeScript struct {
	Proposals []FakeProposal `json:"proposals"`
}

// FakeAdapter is a deterministic, offline adapter for contract tests and
// walkthroughs. It never makes provider calls.
type FakeAdapter struct {
	Info        AdapterInfo
	Proposals   []FakeProposal
	DescribeErr error

	mu       sync.Mutex
	next     int
	requests []ProposeRequest
}

// DefaultFakeInfo describes the built-in fake adapter.
func DefaultFakeInfo() AdapterInfo {
	return AdapterInfo{
		Name:                 "fake",
		Version:              "1.0.0",
		ContractVersions:     []string{ContractVersion},
		RequiredCapabilities: []string{CapProposeFiles, CapFeedbackOptimization},
		OptionalCapabilities: []string{CapTargetContextOverlay, CapTargetSkillDirectory, CapFeedbackTaskResults},
	}
}

// NewFakeAdapter returns a fake adapter with the default description.
func NewFakeAdapter(proposals ...FakeProposal) *FakeAdapter {
	return &FakeAdapter{Info: DefaultFakeInfo(), Proposals: proposals}
}

// LoadFakeScript parses a strict fake-adapter script.
func LoadFakeScript(path string) (*FakeAdapter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading fake script: %w", err)
	}
	var script FakeScript
	if err := decodeStrict(data, &script); err != nil {
		return nil, fmt.Errorf("parsing fake script %s: %w", path, err)
	}
	return NewFakeAdapter(script.Proposals...), nil
}

// Requests returns copies of every Propose request received.
func (f *FakeAdapter) Requests() []ProposeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *FakeAdapter) Describe(context.Context) (AdapterInfo, error) {
	return f.Info, f.DescribeErr
}

func (f *FakeAdapter) Propose(ctx context.Context, req ProposeRequest) (ProposeResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	if f.next >= len(f.Proposals) {
		f.mu.Unlock()
		return ProposeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion, Done: true}, nil
	}
	step := f.Proposals[f.next]
	f.next++
	f.mu.Unlock()

	switch {
	case step.Hang:
		<-ctx.Done()
		return ProposeResponse{}, context.Cause(ctx)
	case step.Error != "":
		return ProposeResponse{}, errors.New(step.Error)
	case step.Raw != nil:
		return *step.Raw, nil
	}
	resp := ProposeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion, Done: step.Done, Usage: step.Usage}
	if len(step.Files) == 0 {
		return resp, nil
	}
	merged := map[string]string{}
	for _, file := range req.Parent.Files {
		merged[file.Path] = file.Content
	}
	maps.Copy(merged, step.Files)
	files := make([]FileContent, 0, len(merged))
	for _, p := range slices.Sorted(maps.Keys(merged)) {
		files = append(files, FileContent{Path: p, Content: merged[p]})
	}
	resp.Candidate = &ProposedCandidate{ParentID: req.Parent.ID, Files: files, Rationale: step.Rationale}
	return resp, nil
}

// decodeStrict decodes exactly one JSON value and rejects unknown fields.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}
