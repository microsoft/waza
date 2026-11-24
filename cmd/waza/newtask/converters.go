package newtask

import (
	"errors"
	"iter"
	"regexp"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

type toolType string

const (
	ToolTypeFunction   = "function"
	ToolTypeBash       = "bash"
	ToolTypePowershell = "powershell"
	ToolTypeView       = "view"
	ToolTypeCreate     = "create"
)

type toolArgs struct {
	Path     string `mapstructure:"path"`      // view, create
	FileText string `mapstructure:"file_text"` // create

	Command     string `mapstructure:"command"`     // bash, powershell
	Description string `mapstructure:"description"` // bash, powershell

	Skill string // skill
}

type tool struct {
	Type toolType

	Start time.Time
	End   time.Time

	Name      string
	Arguments toolArgs
	Success   bool
}

type skill struct {
	Name string
	Path string
}

type CreateTestCaseFromCopilotLogOptions struct {
	DisplayName string
	TestID      string
	Tags        []string
}

func CreateTestCaseFromCopilotLog(copilotLog string, options *CreateTestCaseFromCopilotLogOptions) (*models.TestCase, error) {
	return createTestCaseFromCopilotEvents(utils.NewCopilotLogIterator(copilotLog), options)
}

// CreateTestCaseFromCopilotEvents generates a task without requiring a persisted session log.
func CreateTestCaseFromCopilotEvents(events []copilot.SessionEvent, options *CreateTestCaseFromCopilotLogOptions) (*models.TestCase, error) {
	return createTestCaseFromCopilotEvents(func(yield func(copilot.SessionEvent, error) bool) {
		for _, event := range events {
			if event.Type() == copilot.SessionEventTypeAssistantMessageDelta {
				continue
			}
			if !yield(event, nil) {
				return
			}
		}
	}, options)
}

func createTestCaseFromCopilotEvents(events iter.Seq2[copilot.SessionEvent, error], options *CreateTestCaseFromCopilotLogOptions) (*models.TestCase, error) {
	if options == nil {
		options = &CreateTestCaseFromCopilotLogOptions{}
	}

	toolsInOrder := []string{}
	tools := map[string]*tool{}
	var skills []skill

	var displayName string

	if options.DisplayName != "" {
		displayName = options.DisplayName
	}

	var testID string

	if options.TestID != "" {
		testID = options.TestID
	}

	// let's compose a single task
	task := &models.TestCase{
		DisplayName: displayName,
		TestID:      testID,
		Tags:        options.Tags,
	}

	responses := &strings.Builder{}

	for e, err := range events {
		if err != nil {
			return nil, err
		}

		switch e.Type() {
		case copilot.SessionEventTypeUserMessage:
			if content, ok := copilotevents.Content(e); ok {
				task.Stimulus.Message = content
			}
		case copilot.SessionEventTypeToolExecutionStart:
			start, ok := copilotevents.ToolStart(e)
			if !ok || start.ToolCallID == "" {
				continue
			}

			toolsInOrder = append(toolsInOrder, start.ToolCallID)

			var ta *toolArgs

			if start.Arguments != nil {
				if err := mapstructure.Decode(start.Arguments, &ta); err != nil {
					return nil, err
				}
			} else {
				ta = &toolArgs{}
			}

			toolName := "<unknown>"

			if start.ToolName != "" {
				toolName = start.ToolName
			}

			tools[start.ToolCallID] = &tool{
				Start:     e.Timestamp,
				Name:      toolName,
				Arguments: *ta,
			}
		case copilot.SessionEventTypeToolExecutionComplete:
			if complete, ok := copilotevents.ToolComplete(e); ok && complete.ToolCallID != "" {
				t, exists := tools[complete.ToolCallID]

				if !exists { // _shouldn't_ happen, but we'll be defensive
					continue
				}

				t.End = e.Timestamp

				t.Success = complete.Success
			}
		case copilot.SessionEventTypeAssistantMessage:
			if content, ok := copilotevents.Content(e); ok {
				responses.WriteString(content)
			}
		case copilot.SessionEventTypeAssistantMessageDelta:
			if content, ok := copilotevents.DeltaContent(e); ok {
				responses.WriteString(content)
			}
		case copilot.SessionEventTypeSkillInvoked:
			if invoked, ok := copilotevents.SkillInvoked(e); ok {
				skills = append(skills, skill{
					Name: invoked.Name,
					Path: invoked.Path,
				})
			}
		}
	}

	if responses.Len() == 0 {
		return nil, errors.New("no assistant response was captured; refusing to generate an always-passing response check")
	}

	if len(skills) > 0 {
		var skillNames []string

		for _, sk := range skills {
			skillNames = append(skillNames, sk.Name)
		}

		task.Validators = append(task.Validators, models.ValidatorInline{
			Identifier: "skills-check",
			Kind:       models.GraderKindSkillInvocation,
			Parameters: models.SkillInvocationGraderParameters{
				RequiredSkills: skillNames,
				Mode:           models.SkillMatchingModeAnyOrder,
			},
		})
	}

	if len(tools) > 0 {
		var toolNames []models.ToolSpecParameters

		for _, id := range toolsInOrder {
			if tools[id].Name == "report_intent" {
				continue
			}

			toolNames = append(toolNames, models.ToolSpecParameters{
				Tool:           tools[id].Name,
				CommandPattern: regexp.QuoteMeta(tools[id].Arguments.Command),
				PathPattern:    regexp.QuoteMeta(tools[id].Arguments.Path),
				SkillPattern:   regexp.QuoteMeta(tools[id].Arguments.Skill),
			})
		}

		task.Validators = append(task.Validators, models.ValidatorInline{
			Identifier: "tools-check",
			Kind:       models.GraderKindToolConstraint,
			Parameters: models.ToolConstraintGraderParameters{
				ExpectTools: toolNames,
			},
		})
	}

	task.Validators = append(task.Validators, models.ValidatorInline{
		Identifier: "check-response",
		Kind:       models.GraderKindText,
		Parameters: models.TextGraderParameters{
			ContainsCS: []string{
				responses.String(),
			},
		},
	})

	return task, nil
}
