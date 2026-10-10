package nativetask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func matchRequest() *execution.ExecutionRequest {
	return &execution.ExecutionRequest{
		ModelID: "observed-test-model", Message: "synthetic prompt",
		NoSkills: true, SkipWorkspaceCapture: true,
		ToolPolicy: &execution.ToolPolicy{Mode: execution.ToolPolicyDenyAll},
		Resources:  []execution.ResourceFile{}, Instructions: []execution.InstructionFile{},
		Context: map[string]any{},
	}
}

func matchSources() []CapturedSource {
	raw := "synthetic offline source\n"
	return []CapturedSource{{Path: "eval.yaml", Bytes: raw, Digest: releasepolicy.SourceDigest([]byte(raw))}}
}

func TestMatchSnapshotExactInputAndFullKey(t *testing.T) {
	_, _, admitted, _ := fixture(t)
	binding, err := admitted.Binding()
	require.NoError(t, err)
	require.NoError(t, MatchSnapshot(context.Background(), admitted, binding.Key, matchRequest(), matchSources()))
	for _, name := range []string{"arm", "eval", "task", "cluster", "trial", "attempt"} {
		t.Run(name, func(t *testing.T) {
			key := binding.Key
			switch name {
			case "arm":
				key.Arm = releasepolicy.Candidate
			case "eval":
				key.EvalID += "-other"
			case "task":
				key.TaskID += "-other"
			case "cluster":
				key.ClusterID += "-other"
			case "trial":
				key.Trial++
			case "attempt":
				key.Attempt++
			}
			require.Error(t, MatchSnapshot(context.Background(), admitted, key, matchRequest(), matchSources()))
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, MatchSnapshot(ctx, admitted, binding.Key, matchRequest(), matchSources()), context.Canceled)
	require.Error(t, MatchSnapshot(context.Background(), nil, binding.Key, matchRequest(), matchSources()))
	require.Error(t, MatchSnapshot(context.Background(), admitted, binding.Key, nil, matchSources()))
}

func TestMatchSnapshotRequestAndSourceRejection(t *testing.T) {
	_, _, admitted, _ := fixture(t)
	binding, err := admitted.Binding()
	require.NoError(t, err)
	for _, name := range []string{
		"model", "reasoning", "message", "context", "resource", "instruction", "workdir",
		"source_dir", "task_name", "task_description", "timeout", "nil_context", "nil_resources", "nil_instructions",
		"missing", "extra", "duplicate", "path_alias", "source_mutation", "digest_mutation",
		"nil_policy", "unrestricted", "skills", "ephemeral", "capture", "session", "workspace",
		"streaming", "message_mode", "cancel_skill", "route_skill", "suppress_skill", "skill_name",
		"skill_paths", "git_resources", "mcp", "mocks", "mocks_base", "negative_timeout", "callback", "tools",
	} {
		t.Run(name, func(t *testing.T) {
			req, sources := matchRequest(), matchSources()
			switch name {
			case "model":
				req.ModelID = "other"
			case "reasoning":
				req.ReasoningEffort = "high"
			case "message":
				req.Message += "different"
			case "context":
				req.Context["large"] = json.Number("90071992547409931234567890")
			case "resource":
				req.Resources = []execution.ResourceFile{{Path: "file", Content: []byte("bytes")}}
			case "instruction":
				req.Instructions = []execution.InstructionFile{{Path: "file", Content: []byte("bytes")}}
			case "workdir":
				req.WorkDir = "work"
			case "source_dir":
				req.SourceDir = "/other"
			case "task_name":
				req.TaskName = "name"
			case "task_description":
				req.TaskDescription = "description"
			case "timeout":
				req.FirstEventTimeout = 1
			case "nil_context":
				req.Context = nil
			case "nil_resources":
				req.Resources = nil
			case "nil_instructions":
				req.Instructions = nil
			case "missing":
				sources = nil
			case "extra":
				sources = append(sources, CapturedSource{Path: "extra", Bytes: "extra"})
			case "duplicate":
				sources = append(sources, sources[0])
			case "path_alias":
				sources[0].Path = "./eval.yaml"
			case "source_mutation":
				sources[0].Bytes += " "
				sources[0].Digest = releasepolicy.SourceDigest([]byte(sources[0].Bytes))
			case "digest_mutation":
				sources[0].Digest.Encoding = "json-v1"
			case "nil_policy":
				req.ToolPolicy = nil
			case "unrestricted":
				req.ToolPolicy = execution.NewToolPolicy(nil)
			case "skills":
				req.NoSkills = false
			case "ephemeral":
				req.EphemeralSession = true
			case "capture":
				req.SkipWorkspaceCapture = false
			case "session":
				req.SessionID = "resume"
			case "workspace":
				req.WorkspaceDir = "reuse"
			case "streaming":
				req.Streaming = true
			case "message_mode":
				req.MessageMode = execution.MessageModeEnqueue
			case "cancel_skill":
				req.CancelOnSkillInvocation = true
			case "route_skill":
				req.TriggerSkillRouting = true
			case "suppress_skill":
				req.SuppressSkillBody = true
			case "skill_name":
				req.SkillName = "skill"
			case "skill_paths":
				req.SkillPaths = []string{"skill"}
			case "git_resources":
				req.GitResources = []models.GitResource{{}}
			case "mcp":
				req.MCPServers = map[string]copilot.MCPServerConfig{"server": nil}
			case "mocks":
				req.CommandMocks = []models.CommandMockConfig{{}}
			case "mocks_base":
				req.CommandMocksBaseDir = "base"
			case "negative_timeout":
				req.FirstEventTimeout = -1
			case "callback":
				req.PermissionHandler = func(copilot.PermissionRequest, copilot.PermissionInvocation) (rpc.PermissionDecision, error) {
					t.Fatal("comparison must never invoke callbacks")
					return nil, errors.New("unexpected callback")
				}
			case "tools":
				req.Tools = []copilot.Tool{{Name: "forbidden"}}
			}
			require.Error(t, MatchSnapshot(context.Background(), admitted, binding.Key, req, sources))
		})
	}
}

func TestProjectRequestPreservesIntentJSON(t *testing.T) {
	req := matchRequest()
	req.Context = map[string]any{
		"integer": json.Number("90071992547409931234567890"),
		"decimal": json.Number("1.00"),
		"nested": struct {
			Z string
			A string
		}{Z: "last", A: "first"},
	}
	intent, total, err := projectRequest(req)
	require.NoError(t, err)
	require.Zero(t, total)
	raw := encoded(t, intent)
	var detached requestIntent
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&detached))
	a, err := canonicalIntent(intent)
	require.NoError(t, err)
	b, err := canonicalIntent(detached)
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Equal(t, digest(t, intent), digest(t, detached))
	sum := sha256.Sum256(a)
	require.Equal(t, hex.EncodeToString(sum[:]), digest(t, intent).SHA256)
	require.Contains(t, string(a), "90071992547409931234567890")
	require.Contains(t, string(a), "1.00")
	require.Equal(t, "deny_all", intent.PermissionMode)
	require.Equal(t, "deny_all", intent.ToolPolicyMode)
	req.Resources = []execution.ResourceFile{{Path: "r", Content: []byte("abc")}}
	req.Instructions = []execution.InstructionFile{{Path: "i", Content: []byte("xy")}}
	_, total, err = projectRequest(req)
	require.NoError(t, err)
	require.Equal(t, 5, total)
	req.Resources[0].Content = nil
	nilContent, _, err := projectRequest(req)
	require.NoError(t, err)
	nilDigest := digest(t, nilContent)
	req.Resources[0].Content = []byte{}
	emptyContent, _, err := projectRequest(req)
	require.NoError(t, err)
	require.NotEqual(t, nilDigest, digest(t, emptyContent))
}

func TestMatchSnapshotDoesNotUpgradePrimitiveCaps(t *testing.T) {
	for _, size := range []int{assurance.MaxLabelBytes + 1, 800 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			_, _, admitted, _ := fixture(t)
			data, err := admitted.prepared.view()
			require.NoError(t, err)
			data.Sources[0].Bytes = make([]byte, size)
			data.Sources[0].Digest = releasepolicy.SourceDigest(data.Sources[0].Bytes)
			// A private fixture simulates a sealed over-limit value; the
			// comparison API has no supplied-seal or preparation setter.
			admitted.prepared.data = encoded(t, data)
			admitted.prepared.seal = digest(t, data)
			binding, err := admitted.Binding()
			require.NoError(t, err)
			sources := []CapturedSource{{Path: "eval.yaml", Bytes: string(data.Sources[0].Bytes), Digest: data.Sources[0].Digest}}
			err = MatchSnapshot(context.Background(), admitted, binding.Key, matchRequest(), sources)
			require.ErrorContains(t, err, "primitive")
		})
	}
}

func TestOriginStillUsesExistingDecodeRecord(t *testing.T) {
	prepared, profile, _, record := fixture(t)
	key := record.Key
	key.Trial, key.Attempt = 2, 3
	admitted, err := Admit(context.Background(), prepared, profile, key)
	require.NoError(t, err)
	record.Key, record.Summary.Key = key, key
	origin := models.EvidenceOrigin{EvalID: key.EvalID, TaskID: key.TaskID, RunNumber: key.Trial, AttemptCount: key.Attempt}
	record.Origin, record.ActualRow.Origin, record.Summary.Origin = origin, origin, origin
	record.ActualRow.Run.RunNumber, record.ActualRow.Run.Attempts = key.Trial, key.Attempt
	decoded, err := DecodeRecord(encoded(t, record), admitted)
	require.NoError(t, err)
	require.Equal(t, Capabilities{"not_assessed", "not_assessed", "not_assessed", "not_assessed", "not_assessed"}, decoded.Capabilities())
	for _, name := range []string{"eval", "task", "run", "attempt", "prior_attempts"} {
		t.Run(name, func(t *testing.T) {
			copy := record
			wrong := origin
			switch name {
			case "eval":
				wrong.EvalID += "other"
			case "task":
				wrong.TaskID += "other"
			case "run":
				wrong.RunNumber++
			case "attempt":
				wrong.AttemptCount++
			case "prior_attempts":
				wrong.PriorAttempts = "invented ordinal authority"
			}
			copy.Origin, copy.ActualRow.Origin, copy.Summary.Origin = wrong, wrong, wrong
			_, err := DecodeRecord(encoded(t, copy), admitted)
			require.Error(t, err)
			require.False(t, errors.Is(err, context.Canceled))
		})
	}
}
