package newtask

import (
	"regexp"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/go-viper/mapstructure/v2"
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

	for e, err := range utils.NewCopilotLogIterator(copilotLog) {
		if err != nil {
			return nil, err
		}

		switch e.Type {
		case copilot.UserMessage:
			if e.Data.Content != nil {
				task.Stimulus.Message = *e.Data.Content
			}
		case copilot.ToolExecutionStart:
			if e.Data.ToolCallID == nil {
				continue
			}

			toolsInOrder = append(toolsInOrder, *e.Data.ToolCallID)

			var ta *toolArgs

			if e.Data.Arguments != nil {
				if err := mapstructure.Decode(e.Data.Arguments, &ta); err != nil {
					return nil, err
				}
			} else {
				ta = &toolArgs{}
			}

			toolName := "<unknown>"

			if e.Data.ToolName != nil { // have yet to see the tool name NOT be filled in, but being defensive.
				toolName = *e.Data.ToolName
			}

			tools[*e.Data.ToolCallID] = &tool{
				Start:     e.Timestamp,
				Name:      toolName,
				Arguments: *ta,
			}
		case copilot.ToolExecutionComplete:
			if e.Data.ToolCallID != nil {
				t, exists := tools[*e.Data.ToolCallID]

				if !exists { // _shouldn't_ happen, but we'll be defensive
					continue
				}

				t.End = e.Timestamp

				if e.Data.Success != nil {
					t.Success = *e.Data.Success
				}
			}
		case copilot.AssistantMessage:
			if e.Data.Content != nil {
				responses.WriteString(*e.Data.Content)
			}
		case copilot.AssistantMessageDelta:
			if e.Data.DeltaContent != nil {
				responses.WriteString(*e.Data.DeltaContent)
			}
		case copilot.SkillInvoked:
			name := e.Data.Name

			if name == nil {
				name = new("<no skill name>")
			}

			path := e.Data.Path
			if path == nil {
				path = new("<no path>")
			}

			skills = append(skills, skill{
				Name: *name,
				Path: *path,
			})
		}
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
