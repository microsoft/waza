package nativetask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

// Frozen verbatim Prepare body from d1ce2134ed84acc1c0995b9d0fd1c60a3fdf4e70,
// internal/nativetask/admission.go. This test-only oracle must not evolve with
// projectRequest; it protects original payload, digest, default and error parity.
func originalPrepare(ctx context.Context, arm releasepolicy.Arm, taskID string, req *execution.ExecutionRequest, sources []Source) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (arm != releasepolicy.Baseline && arm != releasepolicy.Candidate) || taskID == "" || req == nil {
		return nil, fmt.Errorf("native preparation requires arm, task and request")
	}
	if req.ModelID == "" || strings.TrimSpace(req.ModelID) != req.ModelID || req.ModelID == "auto" ||
		strings.EqualFold(req.ModelID, "auto") || strings.EqualFold(req.ModelID, "unknown") ||
		req.Message == "" || !req.NoSkills || !req.SkipWorkspaceCapture || req.EphemeralSession ||
		req.PermissionHandler != nil || len(req.Tools) != 0 || req.ToolPolicy == nil ||
		req.ToolPolicy.Mode != execution.ToolPolicyDenyAll || req.SessionID != "" || req.WorkspaceDir != "" ||
		req.Streaming || req.MessageMode != "" || req.CancelOnSkillInvocation || req.TriggerSkillRouting || req.SuppressSkillBody ||
		req.SkillName != "" || len(req.SkillPaths) != 0 || len(req.GitResources) != 0 ||
		len(req.MCPServers) != 0 || len(req.CommandMocks) != 0 || req.CommandMocksBaseDir != "" ||
		req.FirstEventTimeout < 0 {
		return nil, fmt.Errorf("native input contains unsupported or unguarded capabilities")
	}
	if req.WorkDir != "" && (req.WorkDir == "." || !filepath.IsLocal(req.WorkDir) || strings.Contains(req.WorkDir, "\\") ||
		filepath.ToSlash(filepath.Clean(req.WorkDir)) != req.WorkDir) {
		return nil, fmt.Errorf("native workdir must be a canonical local relative path")
	}
	seen := map[string]bool{}
	total := 0
	for _, resource := range req.Resources {
		if resource.Path == "." || !filepath.IsLocal(resource.Path) || strings.Contains(resource.Path, "\\") ||
			filepath.ToSlash(filepath.Clean(resource.Path)) != resource.Path || seen[resource.Path] {
			return nil, fmt.Errorf("native resource path is invalid or duplicated")
		}
		seen[resource.Path] = true
		total += len(resource.Content)
	}
	instructionPaths := map[string]bool{}
	for _, instruction := range req.Instructions {
		if instruction.Path == "." || !filepath.IsLocal(instruction.Path) || strings.Contains(instruction.Path, "\\") ||
			filepath.ToSlash(filepath.Clean(instruction.Path)) != instruction.Path || instructionPaths[instruction.Path] {
			return nil, fmt.Errorf("native instruction path is invalid or duplicated")
		}
		instructionPaths[instruction.Path] = true
		total += len(instruction.Content)
	}
	intent := requestIntent{Model: req.ModelID, Reasoning: req.ReasoningEffort, Message: req.Message,
		WorkDir: req.WorkDir, SourceDir: req.SourceDir, TaskName: req.TaskName, TaskDescription: req.TaskDescription,
		Context: req.Context, Resources: req.Resources, Instructions: req.Instructions,
		FirstEventTimeout: int64(req.FirstEventTimeout), PermissionMode: "deny_all", ToolPolicyMode: "deny_all"}
	data := preparedData{Arm: arm, TaskID: taskID, Request: intent, Sources: []sourceBytes{}}
	last := ""
	if len(sources) == 0 {
		return nil, fmt.Errorf("native preparation requires exact source bytes")
	}
	for _, source := range sources {
		if source.Root == nil || source.Path <= last || !filepath.IsLocal(source.Path) {
			return nil, fmt.Errorf("native sources require rooted unique ordered paths")
		}
		content, err := readSource(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("reading native source %s: %w", source.Path, err)
		}

		total += len(content)
		data.Sources = append(data.Sources, sourceBytes{source.Path, releasepolicy.SourceDigest(content), content})
		last = source.Path
	}
	if total > assurance.MaxLabelBytes {
		return nil, fmt.Errorf("native inputs exceed bounded inventory")
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("freezing native inputs: %w", err)
	}
	if len(payload) > assurance.MaxLabelBytes {
		return nil, fmt.Errorf("native encoded inventory exceeds bounded input")
	}
	seal, err := evidence.JSONDigest(data)
	if err != nil {
		return nil, err
	}
	requestDigest, err := evidence.JSONDigest(intent)
	if err != nil {
		return nil, err
	}
	return &Prepared{payload, *seal, *requestDigest}, nil
}

func TestPrepareFrozenSourceParity(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "eval.yaml"), []byte("source\n"), 0600))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	for _, name := range []string{
		"empty_containers", "nil_containers", "large_number", "nested_struct", "invalid_number", "unencodable",
		"unguarded_before_workdir", "workdir_before_resource", "resource_before_instruction",
		"duplicate_resource", "duplicate_instruction", "negative_timeout", "no_source", "source_alias",
		"raw_cap", "encoded_cap", "cancel", "supported_fields",
	} {
		t.Run(name, func(t *testing.T) {
			req := matchRequest()
			sources := []Source{{Root: root, Path: "eval.yaml"}}
			ctx := context.Background()
			switch name {
			case "supported_fields":
				req.ReasoningEffort = "high"
				req.WorkDir, req.SourceDir = "work", "/original"
				req.TaskName, req.TaskDescription = "name", "description"
				req.FirstEventTimeout = 123
				req.Resources = []execution.ResourceFile{{Path: "resource", Content: []byte("raw")}}
				req.Instructions = []execution.InstructionFile{{Path: "instruction", Content: []byte("inert")}}
			case "nil_containers":
				req.Context, req.Resources, req.Instructions = nil, nil, nil
			case "large_number":
				req.Context["n"] = json.Number("90071992547409931234567890")
			case "nested_struct":
				req.Context["object"] = struct{ Z, A string }{"last", "first"}
			case "invalid_number":
				req.Context["n"] = json.Number("not-a-number")
			case "unencodable":
				req.Context["callback"] = func() {}
			case "unguarded_before_workdir":
				req.NoSkills = false
				req.WorkDir = "."
			case "workdir_before_resource":
				req.WorkDir = "."
				req.Resources = []execution.ResourceFile{{Path: "."}}
			case "resource_before_instruction":
				req.Resources = []execution.ResourceFile{{Path: "."}}
				req.Instructions = []execution.InstructionFile{{Path: "."}}
			case "duplicate_resource":
				req.Resources = []execution.ResourceFile{{Path: "r"}, {Path: "r"}}
			case "duplicate_instruction":
				req.Instructions = []execution.InstructionFile{{Path: "i"}, {Path: "i"}}
			case "negative_timeout":
				req.FirstEventTimeout = -1
			case "no_source":
				sources = nil
			case "source_alias":
				sources[0].Path = "./eval.yaml"
			case "raw_cap":
				req.Resources = []execution.ResourceFile{{Path: "large", Content: make([]byte, assurance.MaxLabelBytes)}}
			case "encoded_cap":
				req.Resources = []execution.ResourceFile{{Path: "large", Content: make([]byte, 800<<10)}}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			before, beforeErr := originalPrepare(ctx, releasepolicy.Baseline, "task", req, sources)
			after, afterErr := Prepare(ctx, releasepolicy.Baseline, "task", req, sources)
			if beforeErr != nil {
				require.EqualError(t, afterErr, beforeErr.Error())
				return
			}
			require.NoError(t, afterErr)
			require.Equal(t, before.data, after.data)
			require.Equal(t, before.seal, after.seal)
			require.Equal(t, before.requestDigest, after.requestDigest)
		})
	}
}
