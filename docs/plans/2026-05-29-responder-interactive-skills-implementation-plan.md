# Responder (Interactive Skills) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-task LLM-backed "responder" that answers an interactive skill's follow-up questions (reply / stop / abstain), driving multi-turn agent runs automatically.

**Architecture:** A new `internal/responder` package owns an LLM surrogate-user session that classifies each agent message via structured tool-calling. The `EvalRunner` owns a follow-up loop (`executeResponderLoop`) that reuses the existing agent session/workspace plumbing (mirroring `executeFollowUps`). Config lives per task under `inputs.responder`; abstain is tagged on the run as a distinct `StatusError` outcome.

**Tech Stack:** Go 1.26, Copilot SDK (`github.com/github/copilot-sdk/go`), `go-viper/mapstructure/v2`, `gopkg.in/yaml.v3`, `santhosh-tekuri/jsonschema/v6`. Tests use the repo's existing `testify`-style assertions and the in-tree `MockEngine` / fake-executor patterns.

**Design doc:** `docs/plans/2026-05-29-responder-interactive-skills.md`

**Conventions:**
- Build: `go build ./...` · Test: `go test ./...` · Lint: `golangci-lint run`
- Run a single test: `go test ./internal/<pkg>/ -run '^TestName$' -v`
- Conventional commits, reference `#303`. Append the trailer:
  `Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>`
  (Commits are signed manually by the maintainer — when a step says "Commit", stage the files and present the commit command; the maintainer runs it.)

---

## File Structure

**Create:**
- `internal/responder/responder.go` — `Classifier`, `Decision`, `DecisionKind`, `New`, `Classify`, decision tools.
- `internal/responder/responder_test.go` — unit tests with a fake executor.

**Modify:**
- `internal/models/testcase.go` — add `ResponderConfig`, `TaskStimulus.Responder`, validation + mutual exclusivity.
- `internal/models/testcase_test.go` — validation tests.
- `internal/models/outcome.go` — add `ResponderInfo` + `RunResult.Responder`.
- `internal/orchestration/runner.go` — classifier factory field, `executeResponderLoop`, branch in `executeRun`, set `RunResult.Responder`/status.
- `internal/orchestration/runner_test.go` (or a new `responder_loop_test.go`) — loop tests with mock engine + fake classifier.
- `schemas/task.schema.json` — add `responder` to the `inputs` `$def`.
- `README.md` — responder section + YAML example.
- `site/src/content/docs/guides/eval-yaml.mdx` and `site/src/content/docs/reference/schema.mdx` — document `inputs.responder`.

**Note on scope:** The dashboard (`web/`) surfacing of `responder.outcome` is included as the final task. The pre-existing placement of `follow_up_prompts` in the task schema (it sits at task top-level, while the Go model reads it under `inputs`) is out of scope — do not "fix" it.

---

## Task 1: Config model — `ResponderConfig` and `TaskStimulus.Responder`

**Files:**
- Modify: `internal/models/testcase.go`
- Test: `internal/models/testcase_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/models/testcase_test.go`:

```go
func TestResponderConfigParsesUnderInputs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "task.yaml")
	yaml := `
id: configure-agent
name: Configure a research agent
inputs:
  prompt: "Add a new agent to my application"
  responder:
    model: gpt-4o
    instructions: |
      The agent you want is research-agent with tools web_search.
      If you can't infer an answer, abstain.
    max_followups: 8
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	tc, err := LoadTestCase(path)
	require.NoError(t, err)
	require.NotNil(t, tc.Stimulus.Responder)
	require.Equal(t, "gpt-4o", tc.Stimulus.Responder.Model)
	require.Equal(t, 8, tc.Stimulus.Responder.MaxFollowups)
	require.Contains(t, tc.Stimulus.Responder.Instructions, "research-agent")
}
```

If `require`/`os`/`filepath` are not already imported in the test file, add them.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/ -run '^TestResponderConfigParsesUnderInputs$' -v`
Expected: FAIL — compile error `tc.Stimulus.Responder undefined` (field does not exist yet).

- [ ] **Step 3: Add the type and field**

In `internal/models/testcase.go`, add the new type after the `TaskStimulus` struct:

```go
// ResponderConfig configures an LLM-backed surrogate user that answers a
// skill's follow-up questions during a multi-turn run. It is mutually
// exclusive with TaskStimulus.FollowUps.
type ResponderConfig struct {
	// Model is the model used for the responder LLM. Optional; when empty the
	// eval-level config.model is used.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// Instructions describe the target configuration the responder represents
	// and the rule for abstaining. Required.
	Instructions string `yaml:"instructions" json:"instructions"`
	// MaxFollowups caps how many times the responder may reply before the loop
	// stops. Required; must be >= 1.
	MaxFollowups int `yaml:"max_followups" json:"max_followups"`
}
```

Then add the field to `TaskStimulus` (place it after `FollowUps`):

```go
	Responder   *ResponderConfig  `yaml:"responder,omitempty" json:"responder,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/models/ -run '^TestResponderConfigParsesUnderInputs$' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/models/testcase.go internal/models/testcase_test.go
git commit -m "feat: add inputs.responder config model #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 2: Config validation — required fields + mutual exclusivity

**Files:**
- Modify: `internal/models/testcase.go` (the `TestCase.Validate` method, ~line 311)
- Test: `internal/models/testcase_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/models/testcase_test.go`:

```go
func TestResponderValidationRejectsMissingInstructions(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{MaxFollowups: 3},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "instructions")
}

func TestResponderValidationRejectsZeroMaxFollowups(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 0},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "max_followups")
}

func TestResponderValidationRejectsBothResponderAndFollowUps(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			FollowUps: []string{"next"},
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 2},
		},
	}
	err := tc.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "follow_up_prompts")
	require.Contains(t, err.Error(), "responder")
}

func TestResponderValidationAcceptsValidConfig(t *testing.T) {
	tc := &TestCase{
		TestID: "t1",
		Stimulus: TaskStimulus{
			Message:   "go",
			Responder: &ResponderConfig{Instructions: "x", MaxFollowups: 2},
		},
	}
	require.NoError(t, tc.Validate())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/models/ -run '^TestResponderValidation' -v`
Expected: FAIL — the "reject" tests get no error (validation not implemented).

- [ ] **Step 3: Implement validation**

In `internal/models/testcase.go`, extend `func (tc *TestCase) Validate() error`. Add the following block before the final `return nil`:

```go
	if r := tc.Stimulus.Responder; r != nil {
		name := tc.TestID
		if name == "" {
			name = tc.DisplayName
		}
		prefix := "test case"
		if name != "" {
			prefix = fmt.Sprintf("test case %q", name)
		}
		if strings.TrimSpace(r.Instructions) == "" {
			return fmt.Errorf("%s: responder.instructions is required", prefix)
		}
		if r.MaxFollowups < 1 {
			return fmt.Errorf("%s: responder.max_followups must be at least 1, got %d", prefix, r.MaxFollowups)
		}
		if len(tc.Stimulus.FollowUps) > 0 {
			return fmt.Errorf("%s: inputs.responder and inputs.follow_up_prompts are mutually exclusive; use one or the other", prefix)
		}
	}
```

(`fmt` and `strings` are already imported in this file.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/models/ -run '^TestResponderValidation' -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add internal/models/testcase.go internal/models/testcase_test.go
git commit -m "feat: validate inputs.responder fields and mutual exclusivity #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 3: Responder package — `Decision` types and tool definitions

**Files:**
- Create: `internal/responder/responder.go`
- Test: `internal/responder/responder_test.go`

This task builds the package skeleton and the three decision tools, verifying that invoking each tool handler records the correct decision. The `Classify` flow is added in Task 4.

- [ ] **Step 1: Write the failing test**

Create `internal/responder/responder_test.go`:

```go
package responder

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/require"
)

func TestDecisionToolsRecordReply(t *testing.T) {
	d := &decisionRecorder{}
	tools := d.tools()
	require.Len(t, tools, 3)

	respond := findTool(t, tools, toolRespond)
	_, err := respond.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"answer": "research-agent"},
	})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionReply, d.decision.Kind)
	require.Equal(t, "research-agent", d.decision.Answer)
}

func TestDecisionToolsRecordStop(t *testing.T) {
	d := &decisionRecorder{}
	stop := findTool(t, d.tools(), toolStop)
	_, err := stop.Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionStop, d.decision.Kind)
}

func TestDecisionToolsRecordAbstain(t *testing.T) {
	d := &decisionRecorder{}
	abstain := findTool(t, d.tools(), toolAbstain)
	_, err := abstain.Handler(copilot.ToolInvocation{
		Arguments: map[string]any{"reason": "brief too vague"},
	})
	require.NoError(t, err)
	require.True(t, d.set)
	require.Equal(t, DecisionAbstain, d.decision.Kind)
	require.Equal(t, "brief too vague", d.decision.Reason)
}

func findTool(t *testing.T, tools []copilot.Tool, name string) copilot.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not found", name)
	return copilot.Tool{}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/responder/ -v`
Expected: FAIL — package/symbols (`decisionRecorder`, `Decision`, `toolRespond`, ...) do not exist.

- [ ] **Step 3: Implement the package skeleton and tools**

Create `internal/responder/responder.go`:

```go
// Package responder implements an LLM-backed surrogate user that answers an
// interactive skill's follow-up questions during a multi-turn evaluation run.
package responder

import (
	"context"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

// DecisionKind enumerates the responder's possible classifications of an agent
// message.
type DecisionKind int

const (
	// DecisionReply means the responder answered the agent's question.
	DecisionReply DecisionKind = iota
	// DecisionStop means the agent is done and no further input is needed.
	DecisionStop
	// DecisionAbstain means the responder could not answer from its brief.
	DecisionAbstain
)

// Decision is the outcome of classifying a single agent message.
type Decision struct {
	Kind   DecisionKind
	Answer string // set when Kind == DecisionReply
	Reason string // set when Kind == DecisionAbstain
}

const (
	toolRespond = "responder_reply"
	toolStop    = "responder_stop"
	toolAbstain = "responder_abstain"
)

// Executor is the narrow execution surface the responder needs. The concrete
// AgentEngine satisfies it, and tests supply a fake.
type Executor interface {
	Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
}

// decisionRecorder captures the single decision tool the responder LLM calls.
type decisionRecorder struct {
	decision Decision
	set      bool
}

func (d *decisionRecorder) tools() []copilot.Tool {
	return []copilot.Tool{
		{
			Name:        toolRespond,
			Description: "Answer the agent's question as the user. Call this exactly once with your answer.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"answer": map[string]any{
						"type":        "string",
						"description": "Your reply to the agent's question, consistent with your configuration.",
					},
				},
				"required": []string{"answer"},
			},
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args struct {
					Answer string `mapstructure:"answer"`
				}
				_ = mapstructure.Decode(inv.Arguments, &args)
				d.decision = Decision{Kind: DecisionReply, Answer: args.Answer}
				d.set = true
				return copilot.ToolResult{}, nil
			},
		},
		{
			Name:        toolStop,
			Description: "Signal that the agent has finished and needs no further input. Call this when there is no question to answer.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(copilot.ToolInvocation) (copilot.ToolResult, error) {
				d.decision = Decision{Kind: DecisionStop}
				d.set = true
				return copilot.ToolResult{}, nil
			},
		},
		{
			Name:        toolAbstain,
			Description: "Signal that you cannot answer the agent's question from your configuration. Call this only when the information is genuinely missing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"reason": map[string]any{
						"type":        "string",
						"description": "Why you cannot answer.",
					},
				},
				"required": []string{"reason"},
			},
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				var args struct {
					Reason string `mapstructure:"reason"`
				}
				_ = mapstructure.Decode(inv.Arguments, &args)
				d.decision = Decision{Kind: DecisionAbstain, Reason: args.Reason}
				d.set = true
				return copilot.ToolResult{}, nil
			},
		},
	}
}

// Classifier is added in the next task; declared here so the package compiles
// with its dependencies referenced.
var _ = models.ResponderConfig{}
```

(The trailing `var _ = models.ResponderConfig{}` keeps the `models` import used until Task 4 adds `Classifier`; remove it in Task 4.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/responder/ -v`
Expected: PASS (three decision-tool tests).

- [ ] **Step 5: Commit**

```bash
git add internal/responder/responder.go internal/responder/responder_test.go
git commit -m "feat: add responder decision types and tools #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 4: Responder package — `Classifier` and `Classify`

**Files:**
- Modify: `internal/responder/responder.go`
- Test: `internal/responder/responder_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/responder/responder_test.go` (add imports `context`, `github.com/microsoft/waza/internal/execution`, `github.com/microsoft/waza/internal/models` as needed):

```go
type fakeExecutor struct {
	calls   []*execution.ExecutionRequest
	respond func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
}

func (f *fakeExecutor) Execute(_ context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	f.calls = append(f.calls, req)
	return f.respond(req)
}

func TestClassifyReply(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			// Simulate the model calling the reply tool.
			_, err := findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": "research-agent"},
			})
			require.NoError(t, err)
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "be research-agent", MaxFollowups: 5}, "gpt-4o")
	d, err := c.Classify(context.Background(), "What is the agent name?")
	require.NoError(t, err)
	require.Equal(t, DecisionReply, d.Kind)
	require.Equal(t, "research-agent", d.Answer)
}

func TestClassifyAbstain(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolAbstain).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"reason": "no info"},
			})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	d, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
	require.Equal(t, DecisionAbstain, d.Kind)
	require.Equal(t, "no info", d.Reason)
}

func TestClassifyNoDecisionToolIsError(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil // no tool called
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q?")
	require.Error(t, err)
}

func TestClassifyUsesDefaultModelWhenUnset(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			require.Equal(t, "default-model", req.ModelID)
			_, _ = findTool(t, req.Tools, toolStop).Handler(copilot.ToolInvocation{Arguments: map[string]any{}})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "x", MaxFollowups: 5}, "default-model")
	_, err := c.Classify(context.Background(), "Q?")
	require.NoError(t, err)
}

func TestClassifyPersistsSession(t *testing.T) {
	exec := &fakeExecutor{
		respond: func(req *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
			_, _ = findTool(t, req.Tools, toolRespond).Handler(copilot.ToolInvocation{
				Arguments: map[string]any{"answer": "a"},
			})
			return &execution.ExecutionResponse{SessionID: "resp-1"}, nil
		},
	}
	c := New(exec, models.ResponderConfig{Instructions: "INSTR", MaxFollowups: 5}, "gpt-4o")
	_, err := c.Classify(context.Background(), "Q1?")
	require.NoError(t, err)
	_, err = c.Classify(context.Background(), "Q2?")
	require.NoError(t, err)

	require.Len(t, exec.calls, 2)
	// First call has no resume session id and includes the instructions.
	require.Empty(t, exec.calls[0].SessionID)
	require.Contains(t, exec.calls[0].Message, "INSTR")
	require.Contains(t, exec.calls[0].Message, "Q1?")
	// Second call resumes the responder session and omits the instructions.
	require.Equal(t, "resp-1", exec.calls[1].SessionID)
	require.NotContains(t, exec.calls[1].Message, "INSTR")
	require.Contains(t, exec.calls[1].Message, "Q2?")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/responder/ -run '^TestClassify' -v`
Expected: FAIL — `New`/`Classify` undefined.

- [ ] **Step 3: Implement `Classifier` and `Classify`**

In `internal/responder/responder.go`: remove the `var _ = models.ResponderConfig{}` line, add `errors` and `fmt` to imports, and append:

```go
// Classifier maintains a persistent surrogate-user session and classifies each
// agent message into a Decision.
type Classifier struct {
	exec         Executor
	model        string
	instructions string
	sessionID    string // empty until the first Classify creates the session
}

// New constructs a Classifier. defaultModel is used when cfg.Model is empty.
func New(exec Executor, cfg models.ResponderConfig, defaultModel string) *Classifier {
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	return &Classifier{
		exec:         exec,
		model:        model,
		instructions: cfg.Instructions,
	}
}

// Classify sends the agent's latest message to the responder LLM and returns
// its decision. The first call seeds the session with the responder
// instructions; subsequent calls resume the same session.
func (c *Classifier) Classify(ctx context.Context, agentMessage string) (Decision, error) {
	rec := &decisionRecorder{}

	req := &execution.ExecutionRequest{
		ModelID:              c.model,
		Message:              c.buildMessage(agentMessage),
		Tools:                rec.tools(),
		MessageMode:          execution.MessageModeEnqueue,
		Streaming:            true,
		SessionID:            c.sessionID,
		NoSkills:             true,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	}

	resp, err := c.exec.Execute(ctx, req)
	if err != nil {
		// If the model already chose a decision before a post-tool follow-up
		// turn failed, honour it (mirrors the prompt grader's behaviour).
		if rec.set {
			return rec.decision, nil
		}
		return Decision{}, fmt.Errorf("responder execution failed: %w", err)
	}
	if resp != nil && resp.SessionID != "" {
		c.sessionID = resp.SessionID
	}
	if !rec.set {
		return Decision{}, errors.New("responder did not call a decision tool")
	}
	return rec.decision, nil
}

func (c *Classifier) buildMessage(agentMessage string) string {
	if c.sessionID == "" {
		return fmt.Sprintf(
			"%s\n\nYou are role-playing as the user. The agent just said:\n\n%s\n\n"+
				"Respond by calling exactly one tool: %s to answer, %s if the agent is finished and needs nothing, or %s if you genuinely cannot answer from your configuration.",
			c.instructions, agentMessage, toolRespond, toolStop, toolAbstain,
		)
	}
	return fmt.Sprintf(
		"The agent just said:\n\n%s\n\nRespond by calling exactly one tool (%s, %s, or %s).",
		agentMessage, toolRespond, toolStop, toolAbstain,
	)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/responder/ -v`
Expected: PASS (all tests, including Task 3's).

- [ ] **Step 5: Commit**

```bash
git add internal/responder/responder.go internal/responder/responder_test.go
git commit -m "feat: add responder Classifier with persistent session #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 5: Results model — `ResponderInfo` on `RunResult`

**Files:**
- Modify: `internal/models/outcome.go`
- Test: `internal/models/outcome_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/models/outcome_test.go`:

```go
func TestResponderInfoSerializes(t *testing.T) {
	rr := RunResult{
		RunNumber: 1,
		Status:    StatusError,
		Responder: &ResponderInfo{
			FollowupsSent: 3,
			Outcome:       "abstained",
			Reason:        "brief too vague",
		},
	}
	data, err := json.Marshal(rr)
	require.NoError(t, err)
	require.Contains(t, string(data), `"responder"`)
	require.Contains(t, string(data), `"outcome":"abstained"`)

	// Omitted when nil.
	data2, err := json.Marshal(RunResult{RunNumber: 1, Status: StatusPassed})
	require.NoError(t, err)
	require.NotContains(t, string(data2), `"responder"`)
}
```

(Add `encoding/json` and `testify/require` imports if not already present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/models/ -run '^TestResponderInfoSerializes$' -v`
Expected: FAIL — `ResponderInfo` / `RunResult.Responder` undefined.

- [ ] **Step 3: Implement the type and field**

In `internal/models/outcome.go`, add after the `RunResult` struct definition:

```go
// ResponderInfo records the outcome of a responder-driven multi-turn run.
type ResponderInfo struct {
	// FollowupsSent is the number of responder answers sent to the agent.
	FollowupsSent int `json:"followups_sent"`
	// Outcome is one of: completed, stopped, abstained, cap_exhausted, error.
	Outcome string `json:"outcome"`
	// Reason holds the responder's reason when Outcome == "abstained" or an
	// error message when Outcome == "error".
	Reason string `json:"reason,omitempty"`
}
```

Add the field to `RunResult` (after `WorkspaceDir`):

```go
	Responder *ResponderInfo `json:"responder,omitempty"`
```

Also add named constants near the top of the file (after the `Status` consts) for reuse by the runner:

```go
// Responder outcome values recorded on RunResult.Responder.Outcome.
const (
	ResponderOutcomeCompleted    = "completed"
	ResponderOutcomeStopped      = "stopped"
	ResponderOutcomeAbstained    = "abstained"
	ResponderOutcomeCapExhausted = "cap_exhausted"
	ResponderOutcomeError        = "error"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/models/ -run '^TestResponderInfoSerializes$' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/models/outcome.go internal/models/outcome_test.go
git commit -m "feat: add ResponderInfo to RunResult #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 6: Runner — classifier factory field (injectable for tests)

**Files:**
- Modify: `internal/orchestration/runner.go`
- Test: `internal/orchestration/runner_test.go`

This task introduces an injectable factory so the loop (Task 7) can be tested with a fake classifier while defaulting to the real responder.

- [ ] **Step 1: Add the abstraction and factory (no behaviour change yet)**

In `internal/orchestration/runner.go`:

1. Add the import `"github.com/microsoft/waza/internal/responder"` to the import block.

2. Add an interface and a factory field to the `EvalRunner` struct. After the `verbose bool` field, add:

```go
	// newClassifier builds a responder classifier for a task. Overridable in
	// tests; defaults to a responder backed by the runner's engine.
	newClassifier func(cfg models.ResponderConfig, defaultModel string) responderClassifier
```

3. Add the interface near the other small type declarations (e.g. just above `type EvalRunner struct`):

```go
// responderClassifier classifies an agent message into a responder decision.
// Implemented by *responder.Classifier; faked in tests.
type responderClassifier interface {
	Classify(ctx context.Context, agentMessage string) (responder.Decision, error)
}
```

4. In `NewEvalRunner` (after the struct is constructed, before `return`), set the default factory:

```go
	r.newClassifier = func(cfg models.ResponderConfig, defaultModel string) responderClassifier {
		return responder.New(r.engine, cfg, defaultModel)
	}
```

(Adjust to match how `NewEvalRunner` names its receiver/local variable — it returns a `*EvalRunner`; assign the field on that value before returning it.)

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./internal/orchestration/`
Expected: success (no behaviour change yet). `responder.New` satisfies `responderClassifier` because `*responder.Classifier` has the `Classify` method.

- [ ] **Step 3: Commit**

```bash
git add internal/orchestration/runner.go
git commit -m "refactor: add injectable responder classifier factory to runner #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 7: Runner — `executeResponderLoop` and branch in `executeRun`

**Files:**
- Modify: `internal/orchestration/runner.go`
- Test: `internal/orchestration/runner_test.go` (or new `internal/orchestration/responder_loop_test.go`)

- [ ] **Step 1: Write the failing tests**

Create `internal/orchestration/responder_loop_test.go`. This uses a fake classifier injected via `r.newClassifier` and a `MockEngine` for agent turns. Adjust the runner/config construction to match existing test helpers in `runner_test.go` (e.g. how other tests build an `EvalRunner` with a spec + `MockEngine`); the assertions below are the contract:

```go
package orchestration

import (
	"context"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/responder"
	"github.com/stretchr/testify/require"
)

// scriptedClassifier returns a queued sequence of decisions.
type scriptedClassifier struct {
	decisions []responder.Decision
	idx       int
	calls     int
}

func (s *scriptedClassifier) Classify(_ context.Context, _ string) (responder.Decision, error) {
	s.calls++
	d := s.decisions[s.idx]
	if s.idx < len(s.decisions)-1 {
		s.idx++
	}
	return d, nil
}

func TestResponderLoopReplyThenStop(t *testing.T) {
	r := newResponderTestRunner(t) // helper: builds EvalRunner with MockEngine + responder spec
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionReply, Answer: "research-agent"},
		{Kind: responder.DecisionStop},
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "be research-agent", MaxFollowups: 5}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeStopped, rr.Responder.Outcome)
	require.Equal(t, 1, rr.Responder.FollowupsSent)
}

func TestResponderLoopAbstainMarksError(t *testing.T) {
	r := newResponderTestRunner(t)
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionAbstain, Reason: "too vague"},
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "x", MaxFollowups: 5}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.Equal(t, models.StatusError, rr.Status)
	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeAbstained, rr.Responder.Outcome)
	require.Contains(t, rr.ErrorMsg, "abstained")
	require.Contains(t, rr.ErrorMsg, "too vague")
}

func TestResponderLoopCapExhausted(t *testing.T) {
	r := newResponderTestRunner(t)
	sc := &scriptedClassifier{decisions: []responder.Decision{
		{Kind: responder.DecisionReply, Answer: "a"}, // always reply
	}}
	r.newClassifier = func(models.ResponderConfig, string) responderClassifier { return sc }

	tc := &models.TestCase{
		TestID:   "t1",
		Stimulus: models.TaskStimulus{Message: "add agent", Responder: &models.ResponderConfig{Instructions: "x", MaxFollowups: 2}},
	}
	rr := r.executeRun(context.Background(), tc, 1)

	require.NotNil(t, rr.Responder)
	require.Equal(t, models.ResponderOutcomeCapExhausted, rr.Responder.Outcome)
	require.Equal(t, 2, rr.Responder.FollowupsSent)
	require.NotEqual(t, models.StatusError, rr.Status) // graded normally
}
```

Add a `newResponderTestRunner(t)` helper in this file. Model it on the existing runner construction in `runner_test.go` — build an `*config.EvalConfig` from a minimal `models.EvalSpec` (skill name, `Config{TrialsPerTask:1, TimeoutSec:30, ModelID:"mock"}`), create a `MockEngine`, call `Initialize`, and return `NewEvalRunner(cfg, engine)`. Inspect `runner_test.go` for the exact helper names already used and reuse them rather than duplicating.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestration/ -run '^TestResponderLoop' -v`
Expected: FAIL — `executeResponderLoop` not wired; `rr.Responder` is nil.

- [ ] **Step 3: Implement the loop and branch**

In `internal/orchestration/runner.go`, change the follow-up branch in `executeRun`. Replace:

```go
	// Execute follow-up prompts if defined
	if len(tc.Stimulus.FollowUps) > 0 {
		r.executeFollowUps(ctx, tc, resp)
	}
```

with:

```go
	// Drive multi-turn: responder loop takes precedence; otherwise static
	// follow-ups. Validation guarantees these are mutually exclusive.
	var responderInfo *models.ResponderInfo
	if tc.Stimulus.Responder != nil {
		responderInfo = r.executeResponderLoop(ctx, tc, resp)
	} else if len(tc.Stimulus.FollowUps) > 0 {
		r.executeFollowUps(ctx, tc, resp)
	}
```

Then, in the `return models.RunResult{...}` at the end of `executeRun`, add the field:

```go
		Responder:        responderInfo,
```

Now add the loop method (place it directly after `executeFollowUps`):

```go
// executeResponderLoop drives a multi-turn run using an LLM-backed surrogate
// user. After each agent turn it classifies the agent's latest message and
// either replies (sending a new agent prompt), stops, or aborts on abstain.
// It mutates resp in place (mirroring executeFollowUps) and returns a summary.
func (r *EvalRunner) executeResponderLoop(ctx context.Context, tc *models.TestCase, resp *execution.ExecutionResponse) *models.ResponderInfo {
	cfg := *tc.Stimulus.Responder
	classifier := r.newClassifier(cfg, r.cfg.Spec().Config.ModelID)

	info := &models.ResponderInfo{Outcome: models.ResponderOutcomeCompleted}
	left := cfg.MaxFollowups
	lastWasReply := false

	for left > 0 {
		decision, err := classifier.Classify(ctx, resp.FinalOutput)
		if err != nil {
			resp.ErrorMsg = fmt.Sprintf("responder error: %v", err)
			info.Outcome = models.ResponderOutcomeError
			info.Reason = err.Error()
			return info
		}

		switch decision.Kind {
		case responder.DecisionStop:
			info.Outcome = models.ResponderOutcomeStopped
			return info

		case responder.DecisionAbstain:
			resp.ErrorMsg = fmt.Sprintf("responder abstained: %s", decision.Reason)
			info.Outcome = models.ResponderOutcomeAbstained
			info.Reason = decision.Reason
			return info

		case responder.DecisionReply:
			if !r.sendResponderReply(ctx, tc, resp, decision.Answer, info.FollowupsSent+1) {
				// sendResponderReply set resp.ErrorMsg; stop the loop.
				info.Outcome = models.ResponderOutcomeError
				info.Reason = resp.ErrorMsg
				return info
			}
			info.FollowupsSent++
			left--
			lastWasReply = true
		}
	}

	if lastWasReply {
		info.Outcome = models.ResponderOutcomeCapExhausted
		slog.WarnContext(ctx, "responder budget exhausted while agent still asking questions",
			"test", tc.DisplayName, "max_followups", cfg.MaxFollowups)
	}
	return info
}

// sendResponderReply sends one responder answer to the agent session, reusing
// the session and workspace, and merges the agent's response into resp. It
// returns false (and sets resp.ErrorMsg) on failure.
func (r *EvalRunner) sendResponderReply(ctx context.Context, tc *models.TestCase, resp *execution.ExecutionResponse, answer string, turn int) bool {
	followReq, err := r.buildExecutionRequest(tc)
	if err != nil {
		resp.ErrorMsg = fmt.Sprintf("responder reply %d setup failed: %v", turn, err)
		return false
	}
	followReq.Message = answer
	followReq.SessionID = resp.SessionID
	followReq.WorkspaceDir = resp.WorkspaceDir

	if r.verbose {
		r.notifyProgress(ProgressEvent{
			EventType: EventAgentPrompt,
			TestName:  tc.DisplayName,
			Details:   map[string]any{"message": answer, "responder_reply": turn},
		})
	}

	timeout, err := r.executionTimeout(tc)
	if err != nil {
		resp.ErrorMsg = fmt.Sprintf("responder reply %d setup failed: %v", turn, err)
		return false
	}
	followCtx, cancelFollow := context.WithTimeout(ctx, timeout)
	followResp, err := r.engine.Execute(followCtx, followReq)
	cancelFollow()
	if err != nil {
		resp.ErrorMsg = fmt.Sprintf("responder reply %d failed: %v", turn, err)
		return false
	}
	if followResp.ErrorMsg != "" {
		resp.ErrorMsg = fmt.Sprintf("responder reply %d: %s", turn, followResp.ErrorMsg)
		return false
	}

	resp.Events = append(resp.Events, followResp.Events...)
	resp.ToolCalls = append(resp.ToolCalls, followResp.ToolCalls...)
	resp.SkillInvocations = append(resp.SkillInvocations, followResp.SkillInvocations...)
	resp.DurationMs += followResp.DurationMs
	resp.FinalOutput = followResp.FinalOutput
	resp.WorkspaceFiles = followResp.WorkspaceFiles
	if followResp.Usage != nil {
		if resp.Usage == nil {
			resp.Usage = followResp.Usage
		} else {
			resp.Usage = models.AggregateUsageStats([]*models.UsageStats{resp.Usage, followResp.Usage})
		}
	}
	return true
}
```

Add `"log/slog"` to the import block if it is not already imported.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/orchestration/ -run '^TestResponderLoop' -v`
Expected: PASS (reply-then-stop, abstain→error, cap-exhausted).

- [ ] **Step 5: Run the full orchestration + models + responder suites**

Run: `go test ./internal/orchestration/ ./internal/models/ ./internal/responder/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/orchestration/runner.go internal/orchestration/responder_loop_test.go
git commit -m "feat: drive interactive skills via responder loop #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 8: JSON schema — add `responder` to the task `inputs` definition

**Files:**
- Modify: `schemas/task.schema.json`
- Test: `internal/validation/schema_test.go` (add a case) — or rely on the existing doc-examples test once docs are added in Task 9.

- [ ] **Step 1: Write the failing test**

Add to `internal/validation/schema_test.go` a test that a task with `inputs.responder` validates. Match the file's existing helper for validating task bytes (look for `ValidateTaskBytes`):

```go
func TestTaskSchemaAcceptsResponder(t *testing.T) {
	yaml := []byte(`
id: t1
name: Configure agent
inputs:
  prompt: "add agent"
  responder:
    instructions: "be research-agent; abstain if unknown"
    max_followups: 8
`)
	errs := ValidateTaskBytes(yaml)
	require.Empty(t, errs)
}
```

(If the exported helper has a different name/signature, use the one already exercised by neighbouring tests in this file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/validation/ -run '^TestTaskSchemaAcceptsResponder$' -v`
Expected: FAIL — `inputs.responder` rejected because the `inputs` `$def` has `additionalProperties: false`.

- [ ] **Step 3: Add `responder` to the schema**

In `schemas/task.schema.json`, inside the `$defs.inputs.properties` object (the block containing `prompt`, `prompt_file`, `context`, `files`, `repos`, `workdir`, `environment`), add:

```json
        "responder": {
          "type": "object",
          "additionalProperties": false,
          "required": ["instructions", "max_followups"],
          "description": "LLM-backed surrogate user that answers the skill's follow-up questions. Mutually exclusive with follow_up_prompts.",
          "properties": {
            "model": {
              "type": "string",
              "description": "Model used for the responder LLM. Defaults to the eval-level config.model."
            },
            "instructions": {
              "type": "string",
              "minLength": 1,
              "description": "Describes the target configuration the responder represents and the rule for abstaining."
            },
            "max_followups": {
              "type": "integer",
              "minimum": 1,
              "description": "Maximum number of responder replies before the loop stops."
            }
          }
        }
```

Insert it as a sibling of `environment` (add a trailing comma to `environment` as needed to keep valid JSON).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/validation/ -run '^TestTaskSchemaAcceptsResponder$' -v`
Expected: PASS. Also run `go test ./internal/validation/` to ensure no regressions.

- [ ] **Step 5: Commit**

```bash
git add schemas/task.schema.json internal/validation/schema_test.go
git commit -m "feat: add inputs.responder to task JSON schema #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 9: Documentation — README and site

**Files:**
- Modify: `README.md`
- Modify: `site/src/content/docs/guides/eval-yaml.mdx`
- Modify: `site/src/content/docs/reference/schema.mdx`

- [ ] **Step 1: Add a README section**

In `README.md`, near where multi-turn / `follow_up_prompts` is described (search for `follow_up_prompts`), add a "Responder (interactive skills)" subsection with this example and explanation:

````markdown
#### Responder (interactive skills)

For skills that ask follow-up questions, configure a `responder` — an LLM that
plays the user and answers the skill's questions. It is mutually exclusive with
`follow_up_prompts`.

```yaml
inputs:
  prompt: "Add a new agent to my application"
  responder:
    model: gpt-4o          # optional; defaults to config.model
    instructions: |
      The agent you want is "research-agent" with system instructions
      "Search the web and summarise findings", tools web_search + url_fetch,
      and no handoffs. Answer the skill's questions consistently with this.
      If you genuinely can't infer an answer, abstain.
    max_followups: 8
```

After each agent turn the responder either **replies** (the answer is sent back,
continuing the conversation), **stops** (the agent is done), or **abstains** —
which fails the run with a distinct `abstained` outcome, signalling the brief is
too vague. Each task carries its own responder, so the same skill can be tested
against several target configurations.
````

- [ ] **Step 2: Add the eval-yaml guide entry**

In `site/src/content/docs/guides/eval-yaml.mdx`, after the `follow_up_prompts` section (search for it), add an analogous `responder` section using the same YAML example and explanation as Step 1, in the file's existing prose style.

- [ ] **Step 3: Add the schema reference entry**

In `site/src/content/docs/reference/schema.mdx`, after the `### follow_up_prompts` section, add:

````markdown
### responder

**Type:** object
**Required:** no

An LLM-backed surrogate user that answers the skill's follow-up questions during
a multi-turn run. Mutually exclusive with `follow_up_prompts`.

| Field          | Type    | Required | Description                                            |
|----------------|---------|----------|--------------------------------------------------------|
| `model`        | string  | no       | Responder model. Defaults to the eval-level `config.model`. |
| `instructions` | string  | yes      | Target configuration the responder represents + abstain rule. |
| `max_followups`| integer | yes      | Max responder replies before the loop stops (`>= 1`).  |

```yaml
inputs:
  prompt: "Add a new agent to my application"
  responder:
    instructions: "Be research-agent with tools web_search; abstain if unknown."
    max_followups: 8
```

The responder classifies each agent message as **reply**, **stop**, or
**abstain**. An abstain marks the run as an error with outcome `abstained`,
distinct from model timeouts or network errors. If `max_followups` is reached
while the agent is still asking questions, the loop stops with outcome
`cap_exhausted` and graders evaluate the final state.
````

- [ ] **Step 4: Build the docs site to verify**

Run: `cd site && npm run build`
Expected: build succeeds with no MDX errors.

- [ ] **Step 5: Commit**

```bash
git add README.md site/src/content/docs/guides/eval-yaml.mdx site/src/content/docs/reference/schema.mdx
git commit -m "docs: document inputs.responder for interactive skills #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 10: Dashboard — surface responder outcome

**Files:**
- Modify: dashboard source under `web/` (locate where `RunResult` / run details are rendered).
- Test: `web/` Playwright tests if the changed view has e2e coverage.

- [ ] **Step 1: Locate where run results are rendered**

Run: `grep -rn "final_output\|error_msg\|workspace_dir" web/src 2>/dev/null | head`
Identify the component that renders per-run details (the same one that shows `error_msg` / status).

- [ ] **Step 2: Render `responder` when present**

In that component, when a run has a `responder` object, display its `outcome`
(and `reason` when present) — e.g. a small badge/label: `Responder: abstained —
brief too vague` or `Responder: cap_exhausted (3 replies)`. Follow the existing
styling/markup conventions in the surrounding component. Keep it read-only; no
new data fetching is required since `responder` is already in the results JSON.

- [ ] **Step 3: Run the dashboard e2e tests (if the view is covered)**

Run: `cd web && npx playwright test --project=chromium`
Expected: PASS. If a screenshot snapshot for this view changed intentionally,
regenerate: `cd web && npx playwright test e2e/screenshots.spec.ts --project=chromium`.

- [ ] **Step 4: Commit**

```bash
git add web/
git commit -m "feat: surface responder outcome in dashboard #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Task 11: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Build everything**

Run: `go build ./...`
Expected: success.

- [ ] **Step 2: Run the full Go test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 3: Lint**

Run: `golangci-lint run`
Expected: no issues. Fix any lint findings in the files you touched.

- [ ] **Step 4: Smoke-test with the mock engine (optional but recommended)**

Create a temporary eval + task using `executor: mock` with an `inputs.responder`
block, run `./waza run <eval.yaml> -v -o /tmp/responder-results.json`, and
confirm the run completes and `responder` appears in the results JSON. Remove
the temp files afterward.

- [ ] **Step 5: Final commit (if any verification fixes were made)**

```bash
git add -A
git commit -m "chore: responder feature verification fixes #303" -m "Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
```

---

## Self-Review Notes (for the planner)

- **Spec coverage:** config surface (Task 1), validation + mutual exclusivity (Task 2), responder package with persistent session + reply/stop/abstain + no-decision error (Tasks 3–4), runner loop + cap-exhaustion + abstain→StatusError (Task 7), `ResponderInfo` reporting (Task 5), schema (Task 8), docs (Task 9), dashboard (Task 10). All design sections map to a task.
- **Type consistency:** `ResponderConfig{Model, Instructions, MaxFollowups}`, `Decision{Kind, Answer, Reason}`, `DecisionReply/Stop/Abstain`, `Classifier.Classify`, `responderClassifier` interface, `ResponderInfo{FollowupsSent, Outcome, Reason}`, and the `ResponderOutcome*` constants are used identically across tasks.
- **Known integration point to verify during execution:** the exact `NewEvalRunner` receiver/return shape (Task 6 Step 1.4) and the existing runner test helper names (Task 7 Step 1) — inspect `runner.go` / `runner_test.go` and adapt the shown code to the real signatures rather than assuming.
