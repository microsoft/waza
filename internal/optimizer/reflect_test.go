package optimizer

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// scriptedEngine is a minimal AgentEngine returning fixed output and
// recording requests.
type scriptedEngine struct {
	output    string
	usage     *models.UsageStats
	requests  []*execution.ExecutionRequest
	initCount int
	shutdown  bool
}

func (e *scriptedEngine) Initialize(context.Context) error { e.initCount++; return nil }
func (e *scriptedEngine) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	e.requests = append(e.requests, req)
	return &execution.ExecutionResponse{FinalOutput: e.output, Success: true, ExecutionSucceeded: true, Usage: e.usage}, nil
}
func (e *scriptedEngine) Shutdown(context.Context) error                 { e.shutdown = true; return nil }
func (e *scriptedEngine) SessionUsage(string) *models.UsageStats         { return nil }
func (e *scriptedEngine) withOutput(out string) *scriptedEngine          { e.output = out; return e }
func (e *scriptedEngine) withUsage(u *models.UsageStats) *scriptedEngine { e.usage = u; return e }

func TestReflectAdapterProposes(t *testing.T) {
	eng := (&scriptedEngine{}).withOutput("thinking...\n<<<WAZA-FILE path=\"g.md\">>>\nnew text\n<<<WAZA-END>>>\n<<<WAZA-RATIONALE>>>\nbecause\n<<<WAZA-END>>>").
		withUsage(&models.UsageStats{InputTokens: 10, OutputTokens: 4})
	a := NewReflectAdapter(eng, "gpt-x")
	req := proposeReq()
	req.History = []Feedback{{CandidateID: req.Parent.ID, Split: SplitOptimization, State: StateScored,
		Tasks: []TaskFeedback{{TestID: "opt-task", Status: "failed", FailedGraders: []GraderFeedback{{Name: "style", Message: "Missing expected substring: upgrade"}}}}}}

	resp, err := a.Propose(context.Background(), req)
	require.NoError(t, err)
	_, err = a.Propose(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 1, eng.initCount, "engine initializes once")
	require.Equal(t, []FileContent{{Path: "g.md", Content: "new text\n"}}, resp.Candidate.Files)
	require.Equal(t, "because", resp.Candidate.Rationale)
	require.Equal(t, req.Parent.ID, resp.Candidate.ParentID)
	require.Equal(t, 10, resp.Usage.InputTokens)

	sent := eng.requests[0]
	require.Equal(t, "gpt-x", sent.ModelID)
	require.True(t, sent.NoSkills)
	require.True(t, sent.EphemeralSession)
	require.NotNil(t, sent.ToolPolicy)
	require.Contains(t, sent.Message, "Missing expected substring: upgrade")
	require.NoError(t, a.Close(context.Background()))
	require.True(t, eng.shutdown)
}

func TestReflectAdapterMalformedOutput(t *testing.T) {
	a := NewReflectAdapter((&scriptedEngine{}).withOutput("I would change the guide."), "m")
	_, err := a.Propose(context.Background(), proposeReq())
	require.Equal(t, CategoryMalformedProposal, CategoryOf(err))
}

func TestReflectAdapterDone(t *testing.T) {
	a := NewReflectAdapter((&scriptedEngine{}).withOutput("All tasks pass.\n<<<WAZA-DONE>>>\n"), "m")
	resp, err := a.Propose(context.Background(), proposeReq())
	require.NoError(t, err)
	require.True(t, resp.Done)
	require.Nil(t, resp.Candidate)
}
