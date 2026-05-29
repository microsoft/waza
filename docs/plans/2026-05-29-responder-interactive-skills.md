# Design: Responder — driving interactive skills via a surrogate LLM

- **Issue:** [#303](https://github.com/microsoft/waza/issues/303)
- **Status:** Approved design, ready for implementation planning
- **Date:** 2026-05-29

## Problem

A growing category of Agent Skills is inherently multi-turn: when the agent
doesn't have everything it needs from the initial prompt, it pauses to ask the
user follow-up questions before completing the task.

**Concrete example — `configure-agent`.** Invoked with "Add a new agent to my
application", the skill must gather a name, system instructions, the set of
tools, and any handoffs before it can generate the agent definition. None of
these can be inferred from the initial prompt, and the structured Q&A *is* part
of the skill's value.

Today, evaluating such a skill in waza forces a bad trade-off: either pre-bake
every answer into the initial prompt (collapsing the skill into a degenerate
one-shot version that no longer tests what ships) or evaluate manually. The
existing static `follow_up_prompts` mechanism only works when the questions —
and their order — are known in advance, which defeats the purpose of testing the
skill's adaptive questioning.

## Goal

Add a **responder**: an LLM-backed surrogate user, configured per task, that
answers the skill's follow-up questions consistently with a described target
configuration. After each agent turn produces chat text, the responder
classifies it into one of three outcomes:

- **Reply** → send the responder's answer back as a new user prompt, decrement a
  follow-up budget, and continue.
- **Stop** → the agent is done (no further questions); exit the loop cleanly.
- **Abstain** → the responder explicitly could not answer. Abort the run with a
  distinct failure classification, signalling that the brief/skill is too vague
  — *not* a transient model timeout or network blip.

## Scope

### In scope

- Per-task `responder` config under `inputs` (sibling to `follow_up_prompts`).
- A responder component that maintains a persistent surrogate-user session and
  classifies each agent message via structured tool-calling.
- Runner integration: a responder-driven follow-up loop reusing the existing
  agent session and workspace.
- Distinct result tagging for abstain and cap-exhaustion, surfaced in logs,
  results JSON, and the dashboard.
- Schema, validation, tests, and documentation.

### Out of scope (possible follow-ups)

- Eval-level responder defaults shared across tasks (each task is self-contained
  for now; if many tasks share one target config, the block repeats).
- Per-field override/merge semantics between eval-level and task-level config.

## User-facing surface

The responder is configured per task under `inputs`, alongside the existing
`follow_up_prompts` field. The two are mutually exclusive.

```yaml
inputs:
  prompt: "Add a new agent to my application"
  responder:
    model: gpt-4o            # optional; defaults to config.model
    instructions: |
      You are configuring a new agent inside an agentic application.
      The agent you want to create has:
        - name: research-agent
        - system instructions: "Search the web and summarise findings on the
          topic the user provides."
        - tools: web_search, url_fetch
        - handoffs: none
      Answer the skill's questions consistently with this configuration,
      regardless of the order in which the skill asks for each piece.
      If you genuinely can't infer an answer from the above, abstain.
    max_followups: 8
```

Because the responder lives per task, the same skill can be exercised against
several target configurations (a research agent, a customer-support agent, a
triage agent with handoffs) by giving each task its own `responder.instructions`
— this is exactly the robustness testing the issue calls for, achieved without
any eval-level override machinery.

### Configuration fields

| Field          | Required | Default          | Notes                                   |
|----------------|----------|------------------|-----------------------------------------|
| `model`        | no       | `config.model`   | Model used for the responder LLM.       |
| `instructions` | yes      | —                | Describes the target config + abstain rule. |
| `max_followups`| yes      | —                | Must be `>= 1`. Caps responder replies. |

## Architecture

The design reuses two patterns already proven in the codebase:

1. **LLM-backed classification via structured tool-calling**, as used by the
   prompt grader (`internal/graders/prompt_grader.go`), against the narrow
   `Executor` interface (`Execute(ctx, *ExecutionRequest)`).
2. **Multi-turn agent follow-ups via session + workspace reuse**, as used by the
   existing static follow-up loop (`executeFollowUps` in
   `internal/orchestration/runner.go`), which resumes the agent session by
   passing `SessionID` and `WorkspaceDir` on each `Execute`.

The responder owns classification; the runner owns the loop and all agent
follow-up plumbing (per-turn timeout, event/usage/tool-call merging).

```mermaid
sequenceDiagram
    participant R as Runner (executeResponderLoop)
    participant A as Agent session (engine)
    participant C as Responder Classifier
    participant S as Responder session (engine)

    R->>A: initial prompt (Execute)
    A-->>R: agent chat text (FinalOutput)
    loop while budget > 0
        R->>C: Classify(agentMessage)
        C->>S: agent question (Execute, persistent session + decision tools)
        S-->>C: tool call: respond / stop / abstain
        C-->>R: Decision
        alt reply
            R->>A: follow-up = answer (Execute, reuse SessionID + WorkspaceDir)
            A-->>R: agent chat text
            Note over R: budget--
        else stop
            Note over R: outcome = stopped; break
        else abstain
            Note over R: outcome = abstained (StatusError); break
        end
    end
    Note over R: budget exhausted while still replying → outcome = cap_exhausted
    R->>R: run graders against final state
```

### Component 1: Config model (`internal/models`)

A new `ResponderConfig` carried on `TaskStimulus` (the `inputs` block):

```go
type ResponderConfig struct {
    Model        string `yaml:"model,omitempty"        json:"model,omitempty"`
    Instructions string `yaml:"instructions"           json:"instructions"`
    MaxFollowups int    `yaml:"max_followups"          json:"max_followups"`
}

// TaskStimulus gains:
//   Responder *ResponderConfig `yaml:"responder,omitempty" json:"responder,omitempty"`
```

**Validation** (in `TestCase.Validate`, surfaced by `LoadTestCase`):

- If `Responder != nil`:
  - `Instructions` must be non-empty.
  - `MaxFollowups >= 1`.
  - `FollowUps` must be empty (mutual exclusivity; clear error message naming
    both fields).

### Component 2: Responder package (`internal/responder`)

```go
type DecisionKind int
const (
    DecisionReply DecisionKind = iota
    DecisionStop
    DecisionAbstain
)

type Decision struct {
    Kind   DecisionKind
    Answer string // set when Kind == DecisionReply
    Reason string // set when Kind == DecisionAbstain
}

// Executor is the narrow execution surface the responder needs (same shape as
// graders.Executor), enabling unit tests with a fake executor.
type Executor interface {
    Execute(ctx context.Context, req *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
}

type Classifier struct {
    exec         Executor
    model        string
    instructions string
    sessionID    string // empty until the first Classify creates the session
}

func New(exec Executor, cfg models.ResponderConfig, defaultModel string) *Classifier
func (c *Classifier) Classify(ctx context.Context, agentMessage string) (Decision, error)
```

`Classify` behaviour:

- **Persistent session.** The first call creates the responder session (no
  resume `SessionID`); the returned `SessionID` is stored and passed on every
  subsequent call so the responder accumulates the back-and-forth like a real
  user. The session is owned by the engine and cleaned up at `Shutdown`.
- **First message** carries the responder `instructions` as a preamble plus the
  agent's first question and a directive to answer by calling exactly one
  decision tool. **Later messages** carry only the agent's latest question
  (instructions persist in session context).
- **Structured output** via three tools whose handlers capture the decision:
  - `respond(answer: string)` → `DecisionReply`
  - `stop()` → `DecisionStop`
  - `abstain(reason: string)` → `DecisionAbstain`
- Request uses `NoSkills: true`, `MessageMode: MessageModeEnqueue`,
  `Streaming: true`. The responder session does **not** use the agent's
  workspace.
- If no decision tool is called (responder malfunction), `Classify` returns an
  error — distinct from abstain.

### Component 3: Runner loop (`internal/orchestration/runner.go`)

In `executeRun`, after the initial `Execute`:

- If `tc.Stimulus.Responder != nil` → `executeResponderLoop`.
- Else if `len(tc.Stimulus.FollowUps) > 0` → existing `executeFollowUps`.

`executeResponderLoop` mirrors `executeFollowUps` plumbing (build request via
`buildExecutionRequest`, set `Message`/`SessionID`/`WorkspaceDir`, apply per-turn
timeout, merge `Events`/`ToolCalls`/`SkillInvocations`/`DurationMs`/`FinalOutput`/
`WorkspaceFiles`/`Usage` into `resp`). Pseudocode:

```
classifier := responder.New(r.engine, *tc.Stimulus.Responder, r.spec.Config.ModelID)
left := tc.Stimulus.Responder.MaxFollowups
sent := 0
outcome := "completed"
for left > 0 {
    decision, err := classifier.Classify(ctx, resp.FinalOutput)
    if err != nil { resp.ErrorMsg = "responder error: " + err; outcome = "error"; break }
    switch decision.Kind {
    case Reply:
        // send agent follow-up using decision.Answer (reuse SessionID + WorkspaceDir)
        // merge follow-up response into resp; on error set resp.ErrorMsg + break
        sent++; left--
        log: responder replied (turn sent, budget left)
    case Stop:
        outcome = "stopped"; goto done
    case Abstain:
        resp.ErrorMsg = "responder abstained: " + decision.Reason
        outcome = "abstained"; goto done
    }
}
if left == 0 && lastDecisionWasReply {
    outcome = "cap_exhausted"
    log warning: responder budget exhausted while agent still asking
}
done:
attach ResponderInfo{FollowupsSent: sent, Outcome: outcome, Reason: ...} to the run
```

Verbose mode emits per-turn progress events (reusing the existing
`EventAgentPrompt` / `EventAgentResponse` style) so `-v` runs show the
responder's answers and the agent's replies.

### Component 4: Results & reporting (`internal/models/outcome.go`)

```go
type ResponderInfo struct {
    FollowupsSent int    `json:"followups_sent"`
    Outcome       string `json:"outcome"` // completed|stopped|abstained|cap_exhausted|error
    Reason        string `json:"reason,omitempty"`
}

// RunResult gains:
//   Responder *ResponderInfo `json:"responder,omitempty"`
```

Status mapping:

| Responder outcome | `RunResult.Status` | `ErrorMsg`                         | Notes |
|-------------------|--------------------|------------------------------------|-------|
| `completed`       | unchanged (graded) | —                                  | Agent finished; graders decide pass/fail. |
| `stopped`         | unchanged (graded) | —                                  | Responder signalled done.            |
| `abstained`       | `StatusError`      | `responder abstained: <reason>`    | Distinct, filterable; separate from timeouts/network errors. |
| `cap_exhausted`   | unchanged (graded) | —                                  | Logged + surfaced; graders judge the end state. |
| `error`           | `StatusError`      | `responder error: <msg>`           | Responder malfunction (no decision / session failure). |

Because abstain reuses `StatusError` but is tagged via `Responder.Outcome`,
reports and the dashboard can distinguish a vague-brief abstain from a genuine
error. The dashboard (`web/`) surfaces `responder.outcome` (and reason) so
abstain and cap-exhaustion are visible per run.

## Error handling & edge cases

- **No decision tool called** → `Classify` error → run `error` outcome
  (`StatusError`), distinct from abstain.
- **Responder session creation/Execute failure** → propagated as run `error`.
- **Agent follow-up Execute failure** → mirrors `executeFollowUps`: set
  `resp.ErrorMsg`, stop the loop.
- **`max_followups` exhausted while agent still asking** → `cap_exhausted`; loop
  stops, run proceeds to grading, warning logged.
- **Mutual exclusivity** of `responder` and `follow_up_prompts` enforced at load
  time with a clear error.
- **Context cancellation / task timeout** honoured on every responder and agent
  turn via the existing per-turn timeout pattern.

## Testing

- **`internal/responder`** — fake `Executor` invoking decision-tool handlers:
  reply / stop / abstain / no-decision-error; persistent-session resumption
  (second `Classify` passes the stored `SessionID`); first-vs-later message
  shape (instructions preamble only on first call); model defaulting.
- **`internal/orchestration`** — mock engine + injectable classifier (or fake
  executor): reply → agent follow-up sent with reused session/workspace; stop;
  abstain → `StatusError` + `Responder.Outcome == "abstained"`; cap exhaustion →
  graded + `Responder.Outcome == "cap_exhausted"`; mutual-exclusivity rejection.
- **`internal/models`** — validation: missing instructions, `max_followups < 1`,
  both `responder` and `follow_up_prompts` set.
- **Schema** — `internal/validation` and `internal/projectconfig` parity tests
  for the new `responder` field.
- All existing tests remain green; `go test ./...` and `golangci-lint run` pass.

## Documentation

Per `AGENTS.md`:

- `README.md` — responder section + YAML example in the eval/inputs docs.
- `site/src/content/docs/` — eval-YAML reference entry for `inputs.responder`
  and a short guide on testing interactive skills; build with `npm run build`.
- Schema files kept in sync.
- Dashboard (`web/`) — surface `responder.outcome`/`reason`; regenerate
  screenshots if UI changes.
- Reference issue #303 in commits; update tracking issue #66 if applicable.

## Rationale

- **Per-task placement** mirrors `follow_up_prompts`, keeps each task
  self-contained, makes mutual-exclusivity checking local, and directly serves
  the "vary the target config across tasks" use case — without any eval-level
  override/merge complexity.
- **Runner owns the loop, responder owns classification** keeps the responder
  small and unit-testable, and reuses the battle-tested agent follow-up plumbing
  rather than duplicating it.
- **Persistent responder session** models a real user who remembers prior
  answers, avoiding contradictory or repeated responses across turns.
- **Abstain as tagged `StatusError`** satisfies the issue's requirement that a
  vague-brief abstain be reportable separately from transient errors, without
  introducing a new top-level status value that every report/consumer would need
  to learn.
