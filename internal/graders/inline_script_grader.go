package graders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	_ "embed"

	"github.com/spboyer/waza/internal/models"
)

type Language string

const (
	LanguagePython Language = "python"
)

//go:embed data/eval_wrapper.py
var evalWrapperPy string

// InlineScriptGrader validates using assertion expressions that represent
// Python snippets.
type InlineScriptGrader struct {
	name       string
	assertions []string
	language   Language
}

type InlineScriptResult struct {
	TotalAssertions  int
	PassedAssertions int
	Failures         []string
}

func NewInlineScriptGrader(name string, language Language, assertions []string) (*InlineScriptGrader, error) {
	switch language {
	case LanguagePython:
	default:
		return nil, fmt.Errorf("language '%s' is not yet supported with inline scripts", language)
	}

	return &InlineScriptGrader{
		name:       name,
		assertions: assertions,
		language:   language,
	}, nil
}

func (isg *InlineScriptGrader) Name() string { return isg.name }
func (isg *InlineScriptGrader) Type() Type   { return TypeInlineScript }

func (isg *InlineScriptGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
	return measureTime(func() (*models.GraderResults, error) {
		if len(isg.assertions) == 0 {
			return &models.GraderResults{
				Name:     isg.name,
				Type:     string(TypeInlineScript),
				Score:    1.0,
				Passed:   true,
				Feedback: "No assertions configured",
			}, nil
		}

		failures, passed, err := runPythonScript(ctx, gradingContext, isg.assertions)

		if err != nil {
			return nil, err
		}

		score := float64(passed) / float64(len(isg.assertions))
		allPassed := len(failures) == 0

		feedback := "All assertions passed"
		if !allPassed {
			feedback = strings.Join(failures, "; ")
		}

		return &models.GraderResults{
			Name:     isg.name,
			Type:     string(TypeInlineScript),
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

func runPythonScript(ctx context.Context, gradingContext *Context, assertions []string) (failures []string, passed int, err error) {
	pythonStdinText, err := getPythonStdinText(gradingContext, assertions)

	if err != nil {
		// let's not quit the entire thing, but we can mark this failure.
		return nil, 0, fmt.Errorf("Failed: script conversion failed for assertions: %w", err)
	}

	tempPythonFile, err := os.CreateTemp("", "temp-python-*.py")

	if err != nil {
		return nil, 0, err
	}

	defer func() {
		_ = os.Remove(tempPythonFile.Name())
	}()

	if _, err := tempPythonFile.Write([]byte(evalWrapperPy)); err != nil {
		return nil, 0, err
	}

	if err := tempPythonFile.Close(); err != nil {
		return nil, 0, err
	}

	// TODO: maybe they have their own python we should use.
	cmd := exec.CommandContext(ctx, "python", tempPythonFile.Name())

	cmd.Stdin = bytes.NewReader(pythonStdinText)
	cmd.Stderr = os.Stderr

	outputBytes, err := cmd.Output()

	if err != nil {
		return nil, 0, fmt.Errorf("failed to execute inline script for assertions (%s): %w", string(outputBytes), err)
	}

	var pythonOutput *struct {
		Results []bool
	}

	if err := json.Unmarshal(outputBytes, &pythonOutput); err != nil {
		return nil, 0, fmt.Errorf("failed to deserialize output (%s) from assertions: %w", string(outputBytes), err)
	}

	// TODO: it might be nice to get more rich results here, but for now it's literally an array
	// as big as assertions, with a true/false value.
	for i, v := range pythonOutput.Results {
		if !v {
			failures = append(failures, fmt.Sprintf("Failed: %s", assertions[i]))
		} else {
			passed++
		}
	}
	return failures, passed, nil
}

func getPythonStdinText(gradingContext *Context, assertions []string) ([]byte, error) {
	/*
	   class Event(TypedDict):
	       role: str
	       content: Any
	       type: str

	   class Data(TypedDict):
	       output: str
	       assertions: list[str]
	       outcome: dict[str, Any]
	       transcript: list[dict[str, Event]]
	       duration_ms: int
	*/

	scriptStdin := struct {
		Output     string                   `json:"output"`
		Outcome    map[string]any           `json:"outcome"`
		Transcript []models.TranscriptEntry `json:"transcript"`
		DurationMS int64                    `json:"duration_ms"`
		Assertions []string                 `json:"assertions"`
	}{
		Output:     gradingContext.Output,
		Outcome:    gradingContext.Outcome,
		Transcript: gradingContext.Transcript,
		DurationMS: gradingContext.DurationMS,
		Assertions: assertions,
	}

	// make life easier for scripters and init values to an empty value, instead of None/nil/null
	if scriptStdin.Transcript == nil {
		scriptStdin.Transcript = []models.TranscriptEntry{}
	}

	if scriptStdin.Outcome == nil {
		scriptStdin.Outcome = map[string]any{}
	}

	scriptJSON, err := json.MarshalIndent(scriptStdin, "  ", "  ")

	if err != nil {
		return nil, err
	}

	return scriptJSON, nil
}
