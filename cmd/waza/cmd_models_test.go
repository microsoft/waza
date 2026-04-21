package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestModelsCommand_ListsModels(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil)
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.ModelInfo{
		{
			ID:   "gpt-4o",
			Name: "GPT-4o",
			Capabilities: copilot.ModelCapabilities{
				Supports: copilot.ModelSupports{Vision: true},
				Limits:   copilot.ModelLimits{MaxContextWindowTokens: 128_000},
			},
		},
		{
			ID:   "claude-sonnet-4",
			Name: "Claude Sonnet 4",
			Capabilities: copilot.ModelCapabilities{
				Supports: copilot.ModelSupports{Vision: false},
				Limits:   copilot.ModelLimits{MaxContextWindowTokens: 200_000},
			},
		},
	}, nil)
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	// Sorted by ID: claude-sonnet-4 first, gpt-4o second
	require.Contains(t, output, "claude-sonnet-4")
	require.Contains(t, output, "gpt-4o")
	require.Contains(t, output, "128k")
	require.Contains(t, output, "200k")
	require.Contains(t, output, "yes") // gpt-4o vision
	require.Contains(t, output, "2 models available")

	// Verify sort order: claude before gpt
	claudeIdx := strings.Index(output, "claude-sonnet-4")
	gptIdx := strings.Index(output, "gpt-4o")
	require.Less(t, claudeIdx, gptIdx, "models should be sorted by ID")
}

func TestModelsCommand_JSONOutput(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil)
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.ModelInfo{
		{ID: "gpt-4o", Name: "GPT-4o"},
	}, nil)
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--json"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	require.Contains(t, output, `"id": "gpt-4o"`)
	require.Contains(t, output, `"name": "GPT-4o"`)
}

func TestModelsCommand_EmptyList(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil)
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.ModelInfo{}, nil)
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.NoError(t, err)

	require.Contains(t, buf.String(), "No models available.")
}

func TestModelsCommand_AuthError(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(nil,
		fmt.Errorf("failed to get copilot authentication status"))
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "copilot login")
}

func TestModelsCommand_NotAuthenticated(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: false,
	}, nil)
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "copilot login")
}

func TestModelsCommand_ListModelsError(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil)
	client.EXPECT().ListModels(gomock.Any()).Return(nil, fmt.Errorf("backend unavailable"))
	client.EXPECT().Stop().Return(nil)

	var buf bytes.Buffer
	cmd := newModelsCommandWithOptions(&modelsCommandOptions{
		NewCopilotClient: func(*copilot.ClientOptions) execution.CopilotClient { return client },
	})
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to list models")
}

func TestFormatTokenCount(t *testing.T) {
	tests := []struct {
		tokens   int
		expected string
	}{
		{128_000, "128k"},
		{200_000, "200k"},
		{1_000_000, "1M"},
		{2_000_000, "2M"},
		{500, "500"},
		{0, "0"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d", tt.tokens), func(t *testing.T) {
			result := formatTokenCount(tt.tokens)
			require.Equal(t, tt.expected, result)
		})
	}
}
