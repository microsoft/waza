package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/copilot-sdk/go/v2/rpc"
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
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.Model{
		{
			ID:   "gpt-4o",
			Name: "GPT-4o",
			Capabilities: copilot.ModelCapabilities{
				Supports: &rpc.ModelCapabilitiesSupports{Vision: copilot.Bool(true)},
				Limits:   &rpc.ModelCapabilitiesLimits{MaxContextWindowTokens: int64Ptr(128_000)},
			},
		},
		{
			ID:   "claude-sonnet-4",
			Name: "Claude Sonnet 4",
			Capabilities: copilot.ModelCapabilities{
				Supports: &rpc.ModelCapabilitiesSupports{Vision: copilot.Bool(false)},
				Limits:   &rpc.ModelCapabilitiesLimits{MaxContextWindowTokens: int64Ptr(200_000)},
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
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.Model{
		{ID: "gpt-4o", Name: "GPT-4o", Billing: &rpc.ModelBilling{
			Multiplier:      copilot.Float64(1.5),
			DiscountPercent: new(int32(10)),
			Promo:           &rpc.ModelBillingPromo{},
			TokenPrices: &rpc.ModelBillingTokenPrices{
				BatchSize:       int64Ptr(1000),
				CacheReadPrice:  copilot.Float64(0.25),
				MaxPromptTokens: int64Ptr(128000),
				LongContext: &rpc.ModelBillingTokenPricesLongContext{
					CacheReadPrice:  copilot.Float64(0.5),
					MaxPromptTokens: int64Ptr(1000000),
				},
			},
		}},
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

	// The --json output preserves the pre-v2 shape: capabilities.supports and
	// capabilities.limits are objects and both support flags are always present.
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &decoded))
	require.Len(t, decoded, 1)
	capabilities, ok := decoded[0]["capabilities"].(map[string]any)
	require.True(t, ok, "capabilities must be an object")
	supports, ok := capabilities["supports"].(map[string]any)
	require.True(t, ok, "capabilities.supports must be an object")
	require.Equal(t, false, supports["vision"])
	require.Equal(t, false, supports["reasoningEffort"])
	limits, ok := capabilities["limits"].(map[string]any)
	require.True(t, ok, "capabilities.limits must be an object")
	require.Empty(t, limits)
	// SDK v2 and runtime 1.0.114 no longer define the deprecated
	// cachePrice/contextMax fields, so they are absent rather than synthesized
	// from cacheReadPrice/maxPromptTokens.
	require.Equal(t, map[string]any{
		"multiplier": 1.5,
		"tokenPrices": map[string]any{
			"batchSize":       float64(1000),
			"cacheReadPrice":  0.25,
			"maxPromptTokens": float64(128000),
			"longContext": map[string]any{
				"cacheReadPrice":  0.5,
				"maxPromptTokens": float64(1000000),
			},
		},
	}, decoded[0]["billing"], "billing keeps multiplier/tokenPrices and omits removed deprecated fields")
}

func int64Ptr(v int64) *int64 { return &v }

func TestModelsCommand_EmptyList(t *testing.T) {
	ctrl := gomock.NewController(t)

	client := NewMockCopilotClient(ctrl)
	client.EXPECT().Start(gomock.Any()).Return(nil)
	client.EXPECT().GetAuthStatus(gomock.Any()).Return(&copilot.GetAuthStatusResponse{
		IsAuthenticated: true,
	}, nil)
	client.EXPECT().ListModels(gomock.Any()).Return([]copilot.Model{}, nil)
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
		tokens   int64
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
