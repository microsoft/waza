package models

import (
	"errors"
	"fmt"

	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/schemaloader"
	"gopkg.in/yaml.v3"
)

// Failed matcher compilation must not erase the remaining typed configuration.
// These values are used only for static validation; every decoding/compilation
// error is retained, so a partial value can never reach native execution.
func decodeOfflineToolParameters(declaration schemaGuardGrader) (GraderParameters, error) {
	switch declaration.Kind {
	case GraderKindToolCalls:
		var raw struct {
			RequiredTools  []string    `yaml:"required_tools"`
			ForbiddenTools []string    `yaml:"forbidden_tools"`
			MinCalls       *int        `yaml:"min_calls"`
			MaxCalls       *int        `yaml:"max_calls"`
			Expect         []yaml.Node `yaml:"expect"`
		}
		if err := declaration.Config.Decode(&raw); err != nil {
			return nil, err
		}
		params := ToolCallsGraderParameters{
			RequiredTools: raw.RequiredTools, ForbiddenTools: raw.ForbiddenTools,
			MinCalls: raw.MinCalls, MaxCalls: raw.MaxCalls,
		}
		if raw.Expect != nil {
			params.Expect = make([]ToolExpectation, len(raw.Expect))
		}
		var errs []error
		for i, node := range raw.Expect {
			var expectation struct {
				Tool string    `yaml:"tool"`
				Args yaml.Node `yaml:"args"`
			}
			if err := node.Decode(&expectation); err != nil {
				errs = append(errs, err)
			}
			args, err := decodeOfflineMatcherArgs(expectation.Args)
			errs = append(errs, err)
			params.Expect[i] = ToolExpectation{Tool: expectation.Tool, Args: args}
		}
		return params, errors.Join(errs...)
	case GraderKindToolConstraint:
		var raw struct {
			ExpectTools []yaml.Node  `yaml:"expect_tools"`
			RejectTools []yaml.Node  `yaml:"reject_tools"`
			AllowOnly   *[]yaml.Node `yaml:"allow_only"`
		}
		if err := declaration.Config.Decode(&raw); err != nil {
			return nil, err
		}
		expect, expectErr := decodeOfflineToolSpecs(raw.ExpectTools)
		reject, rejectErr := decodeOfflineToolSpecs(raw.RejectTools)
		params := ToolConstraintGraderParameters{ExpectTools: expect, RejectTools: reject}
		var allowErr error
		if raw.AllowOnly != nil {
			allow, err := decodeOfflineToolSpecs(*raw.AllowOnly)
			params.AllowOnly, allowErr = &allow, err
		}
		return params, errors.Join(expectErr, rejectErr, allowErr)
	default:
		return nil, fmt.Errorf("offline tool parameters require a tool grader, got %q", declaration.Kind)
	}
}

func decodeOfflineToolSpecs(nodes []yaml.Node) ([]ToolSpecParameters, error) {
	if nodes == nil {
		return nil, nil
	}
	specs := make([]ToolSpecParameters, len(nodes))
	var errs []error
	for i, node := range nodes {
		var raw struct {
			Tool           string    `yaml:"tool"`
			CommandPattern string    `yaml:"command_pattern"`
			SkillPattern   string    `yaml:"skill_pattern"`
			PathPattern    string    `yaml:"path_pattern"`
			Args           yaml.Node `yaml:"args"`
		}
		if err := node.Decode(&raw); err != nil {
			errs = append(errs, err)
		}
		args, err := decodeOfflineMatcherArgs(raw.Args)
		errs = append(errs, err)
		specs[i] = ToolSpecParameters{
			Tool: raw.Tool, CommandPattern: raw.CommandPattern,
			SkillPattern: raw.SkillPattern, PathPattern: raw.PathPattern, Args: args,
		}
	}
	return specs, errors.Join(errs...)
}

func decodeOfflineMatcherArgs(node yaml.Node) (map[string]argmatcher.Matcher, error) {
	if node.Kind == 0 {
		return nil, nil
	}
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil
	}
	matchers := make(map[string]argmatcher.Matcher, len(raw))
	var errs []error
	for name, value := range raw {
		var matcher argmatcher.Matcher
		if err := matcher.DecodeYAMLWithLoader(&value, schemaloader.Offline{}); err != nil {
			errs = append(errs, fmt.Errorf("argument %q: %w", name, err))
		}
		matchers[name] = matcher
	}
	return matchers, errors.Join(errs...)
}
