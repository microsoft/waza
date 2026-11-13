package optimizer

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/execution"
)

// ReflectAdapter is the reference adapter: a GEPA-style reflective rewrite
// that asks a Copilot model to revise the incumbent candidate from
// optimization-split feedback. It runs with every tool and skill disabled and
// never sees qualification results. It makes paid model calls.
type ReflectAdapter struct {
	Engine execution.AgentEngine
	Model  string

	once    sync.Once
	initErr error
}

const reflectAdapterName = "copilot-reflect"

// NewReflectAdapter wraps an engine. The caller owns engine shutdown via Close.
func NewReflectAdapter(engine execution.AgentEngine, model string) *ReflectAdapter {
	return &ReflectAdapter{Engine: engine, Model: model}
}

func (a *ReflectAdapter) Describe(context.Context) (AdapterInfo, error) {
	return AdapterInfo{
		Name:                 reflectAdapterName,
		Version:              "1.0.0",
		ContractVersions:     []string{ContractVersion},
		RequiredCapabilities: []string{CapProposeFiles, CapFeedbackOptimization, CapFeedbackTaskResults},
		OptionalCapabilities: []string{CapTargetSkillDirectory, CapTargetContextOverlay, CapAdapterUsageReporting},
		PaidCalls:            true,
	}, nil
}

// Close shuts the engine down.
func (a *ReflectAdapter) Close(ctx context.Context) error {
	if a.Engine == nil {
		return nil
	}
	return execution.ShutdownBounded(ctx, a.Engine, execution.DefaultShutdownGrace, execution.DefaultShutdownTimeout)
}

func (a *ReflectAdapter) Propose(ctx context.Context, req ProposeRequest) (ProposeResponse, error) {
	a.once.Do(func() { a.initErr = a.Engine.Initialize(ctx) })
	if a.initErr != nil {
		return ProposeResponse{}, fmt.Errorf("initializing reflect engine: %w", a.initErr)
	}
	resp, err := a.Engine.Execute(ctx, &execution.ExecutionRequest{
		ModelID:              a.Model,
		Message:              reflectPrompt(req),
		ToolPolicy:           execution.NewToolPolicy(&[]string{}),
		NoSkills:             true,
		EphemeralSession:     true,
		SkipWorkspaceCapture: true,
	})
	out := ProposeResponse{Kind: AdapterResponseKind, ContractVersion: ContractVersion}
	// Record measured usage before handling errors so a failed paid call
	// still counts toward adapter spend.
	if resp != nil && resp.Usage != nil {
		out.Usage = &Usage{
			InputTokens:     resp.Usage.InputTokens,
			OutputTokens:    resp.Usage.OutputTokens,
			PremiumRequests: resp.Usage.PremiumRequests,
			AICredits:       resp.Usage.AICredits,
		}
	}
	if err != nil {
		return out, err
	}
	if resp == nil {
		return out, fmt.Errorf("reflect engine returned no response")
	}
	if !resp.ExecutionSucceeded && resp.ErrorMsg != "" {
		return out, fmt.Errorf("reflect engine: %s", resp.ErrorMsg)
	}
	if reflectDonePattern.MatchString(resp.FinalOutput) && !reflectFilePattern.MatchString(resp.FinalOutput) {
		out.Done = true
		return out, nil
	}
	files, rationale, err := parseReflectOutput(resp.FinalOutput, req.Parent.Files)
	if err != nil {
		return out, err
	}
	out.Candidate = &ProposedCandidate{ParentID: req.Parent.ID, Files: files, Rationale: rationale}
	return out, nil
}

var (
	reflectFilePattern      = regexp.MustCompile(`(?s)<<<WAZA-FILE path="([^"\n]+)">>>\n(.*?)\n?<<<WAZA-END>>>`)
	reflectRationalePattern = regexp.MustCompile(`(?s)<<<WAZA-RATIONALE>>>\n?(.*?)\n?<<<WAZA-END>>>`)
	reflectDonePattern      = regexp.MustCompile(`(?m)^\s*<<<WAZA-DONE>>>\s*$`)
)

// parseReflectOutput applies full-file replacements to the parent. Missing
// markers are malformed, never silently treated as "no change".
func parseReflectOutput(output string, parent []FileContent) ([]FileContent, string, error) {
	matches := reflectFilePattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil, "", newError(CategoryMalformedProposal, "reflect output contained no <<<WAZA-FILE>>> blocks")
	}
	files := map[string]string{}
	order := []string{}
	for _, f := range parent {
		files[f.Path] = f.Content
		order = append(order, f.Path)
	}
	for _, m := range matches {
		if _, ok := files[m[1]]; !ok {
			order = append(order, m[1])
		}
		files[m[1]] = m[2] + "\n"
	}
	result := make([]FileContent, 0, len(order))
	for _, p := range order {
		result = append(result, FileContent{Path: p, Content: files[p]})
	}
	rationale := ""
	if m := reflectRationalePattern.FindStringSubmatch(output); m != nil {
		rationale = strings.TrimSpace(m[1])
	}
	return result, rationale, nil
}

func reflectPrompt(req ProposeRequest) string {
	var b strings.Builder
	b.WriteString("You are improving an agent ")
	if req.Target == TargetSkillDirectory {
		b.WriteString("skill (SKILL.md and supporting files)")
	} else {
		b.WriteString("context file set")
	}
	b.WriteString(" so it scores higher on an evaluation suite. Do not use tools. ")
	b.WriteString("Generalize from the failures below; do not hard-code task identifiers or expected strings.\n\n")
	b.WriteString("## Current files\n")
	for _, f := range req.Parent.Files {
		fmt.Fprintf(&b, "<<<WAZA-FILE path=%q>>>\n%s\n<<<WAZA-END>>>\n", f.Path, strings.TrimRight(f.Content, "\n"))
	}
	b.WriteString("\n## Evaluation feedback (optimization tasks only)\n")
	for _, fb := range req.History {
		score := "unscored"
		if fb.Score != nil {
			score = fmt.Sprintf("%.3f", *fb.Score)
		}
		marker := ""
		if fb.CandidateID == req.Parent.ID {
			marker = " (current files)"
		}
		fmt.Fprintf(&b, "- candidate %s%s: state=%s score=%s\n", shortID(fb.CandidateID), marker, fb.State, score)
		if fb.CandidateID != req.Parent.ID {
			continue
		}
		for _, task := range fb.Tasks {
			fmt.Fprintf(&b, "  - task %s: %s (score %.2f)\n", task.TestID, task.Status, task.Score)
			for _, g := range task.FailedGraders {
				fmt.Fprintf(&b, "    - %s: %s\n", g.Name, g.Message)
			}
		}
	}
	b.WriteString("\n## Output format\nReturn every file you change in full, exactly as:\n")
	b.WriteString("<<<WAZA-FILE path=\"relative/path\">>>\n<full file content>\n<<<WAZA-END>>>\n")
	b.WriteString("Then one short explanation:\n<<<WAZA-RATIONALE>>>\n<why this should help>\n<<<WAZA-END>>>\n")
	b.WriteString("If no change is likely to improve the score, reply with only this line instead:\n<<<WAZA-DONE>>>\n")
	return b.String()
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
