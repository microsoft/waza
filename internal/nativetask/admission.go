package nativetask

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

type Source struct {
	Root *os.Root
	Path string
}

type sourceBytes struct {
	Path   string                `json:"path"`
	Digest models.EvidenceDigest `json:"digest"`
	Bytes  []byte                `json:"bytes"`
}

type requestIntent struct {
	Model, Reasoning, Message, WorkDir, SourceDir, TaskName, TaskDescription string
	Context                                                                  map[string]any
	Resources                                                                []execution.ResourceFile
	Instructions                                                             []execution.InstructionFile
	FirstEventTimeout                                                        int64
	PermissionMode, ToolPolicyMode                                           string
}

type preparedData struct {
	Arm     releasepolicy.Arm
	TaskID  string
	Request requestIntent
	Sources []sourceBytes
}

type Prepared struct {
	data          []byte
	seal          models.EvidenceDigest
	requestDigest models.EvidenceDigest
}

type Admitted struct {
	prepared *Prepared
	binding  Admission
	seal     models.EvidenceDigest
}

// Prepare freezes supplied resolved inputs, not their native source resolution.
// The future producer must independently verify declaration/request equivalence.
func Prepare(ctx context.Context, arm releasepolicy.Arm, taskID string, req *execution.ExecutionRequest, sources []Source) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if (arm != releasepolicy.Baseline && arm != releasepolicy.Candidate) || taskID == "" || req == nil {
		return nil, fmt.Errorf("native preparation requires arm, task and request")
	}
	intent, total, err := projectRequest(req)
	if err != nil {
		return nil, err
	}
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

// projectRequest is shared by preparation and read-only comparison. It retains
// the original validation order, projection, defaults and byte accounting.
func projectRequest(req *execution.ExecutionRequest) (requestIntent, int, error) {
	if req == nil {
		return requestIntent{}, 0, fmt.Errorf("native preparation requires arm, task and request")
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
		return requestIntent{}, 0, fmt.Errorf("native input contains unsupported or unguarded capabilities")
	}
	if req.WorkDir != "" && (req.WorkDir == "." || !filepath.IsLocal(req.WorkDir) || strings.Contains(req.WorkDir, "\\") ||
		filepath.ToSlash(filepath.Clean(req.WorkDir)) != req.WorkDir) {
		return requestIntent{}, 0, fmt.Errorf("native workdir must be a canonical local relative path")
	}
	seen := map[string]bool{}
	total := 0
	for _, resource := range req.Resources {
		if resource.Path == "." || !filepath.IsLocal(resource.Path) || strings.Contains(resource.Path, "\\") ||
			filepath.ToSlash(filepath.Clean(resource.Path)) != resource.Path || seen[resource.Path] {
			return requestIntent{}, 0, fmt.Errorf("native resource path is invalid or duplicated")
		}
		seen[resource.Path] = true
		total += len(resource.Content)
	}
	instructionPaths := map[string]bool{}
	for _, instruction := range req.Instructions {
		if instruction.Path == "." || !filepath.IsLocal(instruction.Path) || strings.Contains(instruction.Path, "\\") ||
			filepath.ToSlash(filepath.Clean(instruction.Path)) != instruction.Path || instructionPaths[instruction.Path] {
			return requestIntent{}, 0, fmt.Errorf("native instruction path is invalid or duplicated")
		}
		instructionPaths[instruction.Path] = true
		total += len(instruction.Content)
	}
	intent := requestIntent{Model: req.ModelID, Reasoning: req.ReasoningEffort, Message: req.Message,
		WorkDir: req.WorkDir, SourceDir: req.SourceDir, TaskName: req.TaskName, TaskDescription: req.TaskDescription,
		Context: req.Context, Resources: req.Resources, Instructions: req.Instructions,
		FirstEventTimeout: int64(req.FirstEventTimeout), PermissionMode: "deny_all", ToolPolicyMode: "deny_all"}
	return intent, total, nil
}

func readSource(ctx context.Context, source Source) ([]byte, error) {
	if filepath.ToSlash(filepath.Clean(source.Path)) != source.Path || strings.Contains(source.Path, "\\") {
		return nil, fmt.Errorf("native source path must be canonical")
	}
	check := func() (os.FileInfo, error) {
		parts := strings.Split(source.Path, "/")
		var info os.FileInfo
		for i := range parts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var err error
			info, err = source.Root.Lstat(strings.Join(parts[:i+1], "/"))
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("native source path contains a symlink")
			}
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("native source must be a regular file")
		}
		return info, nil
	}
	before, err := check()
	if err != nil {
		return nil, err
	}
	content, err := assurance.ReadDocument(ctx, source.Root, source.Path, assurance.MaxLabelBytes)
	if err != nil {
		return nil, err
	}
	after, err := check()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("native source changed while freezing")
	}
	return content, nil
}

func (p *Prepared) view() (*preparedData, error) {
	if p == nil {
		return nil, fmt.Errorf("missing sealed preparation")
	}
	var data preparedData
	decoder := json.NewDecoder(bytes.NewReader(p.data))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}
	digest, err := evidence.JSONDigest(data)
	if err != nil || digest == nil || *digest != p.seal {
		return nil, fmt.Errorf("native preparation seal changed")
	}
	req, err := evidence.JSONDigest(data.Request)
	if err != nil || req == nil || *req != p.requestDigest {
		return nil, fmt.Errorf("native request seal changed")
	}
	return &data, nil
}

func (p *Prepared) RequestDigest() models.EvidenceDigest { return p.requestDigest }

// Admit checks internal linkage only, not current policy allocation or sources.
func Admit(ctx context.Context, p *Prepared, profile *Profile, key releasepolicy.AttemptKey) (*Admitted, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := p.view()
	if err != nil {
		return nil, err
	}
	if profile == nil || key.Arm != data.Arm || key.TaskID != data.TaskID || key.EvalID == "" ||
		key.ClusterID == "" || key.Trial < 1 || key.Attempt < 1 {
		return nil, fmt.Errorf("native admission key is not prepared")
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return nil, err
	}
	checked, err := DecodeProfile(raw)
	if err != nil {
		return nil, err
	}
	arm := checked.Arms[key.Arm]
	found := false
	for _, request := range arm.Requests {
		if request.TaskID == key.TaskID && request.RequestDigest == p.requestDigest {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("native request absent from selected profile")
	}
	binding := Admission{checked.Digest.SHA256, checked.PolicyDigest, checked.Digest, arm.PlanDigest, p.requestDigest, key}
	seal, err := evidence.JSONDigest(binding)
	if err != nil {
		return nil, err
	}
	copy := &Prepared{bytes.Clone(p.data), p.seal, p.requestDigest}
	return &Admitted{copy, binding, *seal}, nil
}

func (a *Admitted) Binding() (Admission, error) {
	if a == nil {
		return Admission{}, fmt.Errorf("missing native admission")
	}
	if _, err := a.prepared.view(); err != nil {
		return Admission{}, err
	}
	digest, err := evidence.JSONDigest(a.binding)
	if err != nil || digest == nil || *digest != a.seal {
		return Admission{}, fmt.Errorf("native admission seal changed")
	}
	return a.binding, nil
}

func validDigest(d models.EvidenceDigest, encoding string) bool {
	decoded, err := hex.DecodeString(d.SHA256)
	return err == nil && len(decoded) == 32 && d.SHA256 == strings.ToLower(d.SHA256) && d.Encoding == encoding
}

func DecodeProfile(raw []byte) (*Profile, error) {
	var p Profile
	if err := decodeExact(raw, &p); err != nil {
		return nil, err
	}
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("native profile must be an object")
	}
	delete(object, "digest")
	digest, err := evidence.JSONDigest(object)
	nonce, nonceErr := hex.DecodeString(p.Nonce)
	if err != nil || digest == nil || *digest != p.Digest || !validDigest(p.Digest, "json-v1") ||
		p.Kind != profileKind || p.Version != version || p.Selection != (Selection{"native_text_task", version}) ||
		p.PermissionMode != "deny_all" || !validDigest(p.PolicyDigest, "json-v1") ||
		nonceErr != nil || len(nonce) != 32 || p.Nonce != strings.ToLower(p.Nonce) || len(p.Arms) != 2 {
		return nil, fmt.Errorf("native profile selection or independent identity is invalid")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		plan, exists := p.Arms[arm]
		if !exists || !validDigest(plan.PlanDigest, "json-v1") || len(plan.Requests) == 0 {
			return nil, fmt.Errorf("native profile needs both complete arm inventories")
		}
		last := ""
		for _, request := range plan.Requests {
			if request.TaskID <= last || !validDigest(request.RequestDigest, "json-v1") {
				return nil, fmt.Errorf("native request inventory must be ordered and unique")
			}
			last = request.TaskID
		}
	}
	if !reflect.DeepEqual(p.Arms[releasepolicy.Baseline].Requests, p.Arms[releasepolicy.Candidate].Requests) {
		left, right := p.Arms[releasepolicy.Baseline].Requests, p.Arms[releasepolicy.Candidate].Requests
		if len(left) != len(right) {
			return nil, fmt.Errorf("native arm task inventories differ")
		}
		for i := range left {
			if left[i].TaskID != right[i].TaskID {
				return nil, fmt.Errorf("native arm task inventories differ")
			}
		}
	}
	return &p, nil
}
