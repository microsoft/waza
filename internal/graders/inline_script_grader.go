package graders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	_ "embed"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/models"
)

type Language string

const (
	LanguagePython     Language = "python"
	LanguageJavascript Language = "javascript"
)

const allAssertionsPassedMsg = "All assertions passed"

//go:embed data/eval_wrapper.py
var evalWrapperPy string

//go:embed data/eval_wrapper.js
var evalWrapperJS string

// InlineScriptGrader validates using assertion expressions evaluated by
// an external script runner (Python or JavaScript).
type InlineScriptGrader struct {
	name       string
	assertions []string

	scriptExt      string
	scriptBin      string
	scriptContents string
}

type InlineScriptResult struct {
	TotalAssertions  int
	PassedAssertions int
	Failures         []string
}

func NewInlineScriptGrader(name string, language Language, assertions []string) (*InlineScriptGrader, error) {
	var g = &InlineScriptGrader{
		name:       name,
		assertions: assertions,
	}

	switch language {
	case LanguagePython:
		g.scriptExt = "py"
		g.scriptBin = resolvePythonBin()
		g.scriptContents = evalWrapperPy
	case LanguageJavascript:
		g.scriptExt = "js"
		g.scriptBin = "node"
		g.scriptContents = evalWrapperJS
	default:
		return nil, fmt.Errorf("language '%s' is not yet supported with inline scripts", language)
	}

	return g, nil
}

func resolvePythonBin() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

func (isg *InlineScriptGrader) Name() string            { return isg.name }
func (isg *InlineScriptGrader) Kind() models.GraderKind { return models.GraderKindInlineScript }

func (isg *InlineScriptGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		if len(isg.assertions) == 0 {
			return &models.GraderResults{
				Name:     isg.name,
				Type:     models.GraderKindInlineScript,
				Score:    1.0,
				Passed:   true,
				Feedback: "No assertions configured",
			}, nil
		}

		failures, passed, err := isg.runScript(ctx, gradingContext)

		if err != nil {
			return nil, err
		}

		score := float64(passed) / float64(len(isg.assertions))
		allPassed := len(failures) == 0

		feedback := allAssertionsPassedMsg
		if !allPassed {
			feedback = strings.Join(failures, "; ")
		}

		return &models.GraderResults{
			Name:     isg.name,
			Type:     models.GraderKindInlineScript,
			Score:    score,
			Passed:   allPassed,
			Feedback: feedback,
			// TODO: can we mapstructure.encode or something similar - this is a contract, like any other.
			Details: map[string]any{
				"total_assertions":  len(isg.assertions),
				"passed_assertions": passed,
				"failures":          failures,
			},
		}, nil
	})
}

func (isg *InlineScriptGrader) runScript(ctx context.Context, gradingContext *Context) (failures []string, passed int, err error) {
	stdinText, err := getStdinTextForScript(gradingContext, isg.assertions)

	if err != nil {
		// let's not quit the entire thing, but we can mark this failure.
		return nil, 0, fmt.Errorf("failed: script conversion failed for assertions: %w", err)
	}

	tempScriptFile, err := os.CreateTemp("", "waza-inline-script-*."+isg.scriptExt)

	if err != nil {
		return nil, 0, err
	}

	defer func() {
		os.Remove(tempScriptFile.Name()) //nolint:errcheck
	}()

	if _, err := tempScriptFile.Write([]byte(isg.scriptContents)); err != nil {
		return nil, 0, err
	}

	if err := tempScriptFile.Close(); err != nil {
		return nil, 0, err
	}

	cmd := exec.CommandContext(ctx, isg.scriptBin, tempScriptFile.Name())

	cmd.Stdin = bytes.NewReader(stdinText)
	cmd.Stderr = os.Stderr

	outputBytes, err := cmd.Output()

	if err != nil {
		return nil, 0, fmt.Errorf("failed to execute inline script for assertions (%s): %w", string(outputBytes), err)
	}

	var snippetOutput *struct {
		Results []string
	}

	if err := json.Unmarshal(outputBytes, &snippetOutput); err != nil {
		return nil, 0, fmt.Errorf("failed to deserialize output (%s) from assertions: %w", string(outputBytes), err)
	}

	// TODO: it might be nice to get more rich results here. Currently the script returns
	// a Results slice with one entry per assertion, where:
	//   ""     => assertion passed
	//   "fail" => assertion failed with no additional message
	//   other  => assertion failed and the value is an error message.
	for i, errMsg := range snippetOutput.Results {
		if errMsg == "fail" {
			failures = append(failures, fmt.Sprintf("Failed: %s", isg.assertions[i]))
		} else if errMsg != "" {
			failures = append(failures, fmt.Sprintf("Failed: %s: %s", isg.assertions[i], errMsg))
		} else {
			passed++
		}
	}
	return failures, passed, nil
}

func getStdinTextForScript(gradingContext *Context, assertions []string) ([]byte, error) {
	var sessionEvents []copilot.SessionEvent

	for _, te := range gradingContext.Transcript {
		sessionEvents = append(sessionEvents, te.SessionEvent)
	}

	outcome := gradingContext.Outcome

	if outcome == nil {
		outcome = map[string]any{}
	}

	transcriptEvents := gradingContext.Transcript

	if transcriptEvents == nil {
		transcriptEvents = []models.TranscriptEvent{}
	}

	toolCalls := models.FilterToolCalls(sessionEvents)

	if toolCalls == nil {
		toolCalls = []models.ToolCall{}
	}

	scriptStdin := struct {
		Output     string                   `json:"output"`
		Outcome    map[string]any           `json:"outcome"`
		Transcript []models.TranscriptEvent `json:"transcript"`
		ToolCalls  []models.ToolCall        `json:"tool_calls"`
		DurationMS int64                    `json:"duration_ms"`
		Assertions []string                 `json:"assertions"`

		// Debug causes the underlying scripts to print, to stderr, their stdin contents.
		Debug bool `json:"debug"`
	}{
		Output:     gradingContext.Output,
		Outcome:    outcome,
		Transcript: transcriptEvents,
		ToolCalls:  toolCalls,
		DurationMS: gradingContext.DurationMS,
		Assertions: assertions,
		Debug:      slog.Default().Enabled(context.Background(), slog.LevelDebug),
	}

	scriptJSON, err := json.MarshalIndent(scriptStdin, "  ", "  ")

	if err != nil {
		return nil, err
	}

	return scriptJSON, nil
}
