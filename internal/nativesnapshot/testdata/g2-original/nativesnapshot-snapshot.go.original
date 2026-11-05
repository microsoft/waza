// Package nativesnapshot resolves a bounded subset of native two-arm CLIENT
// inputs without constructing or calling an engine. It is not runtime admission,
// filesystem authenticity, executed-instruction invariance or a public profile.
package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// ArmLocation borrows one caller-owned rooted location per arm. All selected
// sources must fit this root; cross-root translation is deliberately unsupported.
// CWD is the absolute original native cwd; ContextDir retains native CLI semantics
// including an empty value defaulting to the eval directory's fixtures directory.
// Executable identifies the caller-selected evaluator file, not runtime version.
type ArmLocation struct {
	Root       *os.Root
	EvalPath   string
	CWD        string
	ContextDir string
	Executable string
}

type boundLocation struct {
	location    ArmLocation
	base        string
	identity    os.FileInfo
	cwdIdentity os.FileInfo
}

type discovery struct {
	Pattern string
	Paths   []string
}

type sourceIdentity struct {
	Path       string
	Roles      []string
	ByteLength int
	Digest     models.EvidenceDigest
}

type taskSnapshot struct {
	Path    string
	ID      string
	Enabled bool
	Native  json.RawMessage
	Request json.RawMessage
}

type armSnapshot struct {
	EvalPath   string
	CWD        string
	ContextDir string
	Executable string
	RootPath   string
	Native     json.RawMessage
	Sources    []sourceIdentity
	Discovery  []discovery
	Tasks      []taskSnapshot
}

// Prepared is opaque and detached from caller declarations and request objects.
// It retains borrowed root handles solely for independent recapture.
// Its private json-v1 seal covers the complete two-arm snapshot, including full
// native CLIENT request JSON. It is not nativetask's narrower requestIntent
// digest, and no request-intent digest or core/policy admission is exposed.
type Prepared struct {
	bindings  map[releasepolicy.Arm]boundLocation
	canonical []byte
	seal      models.EvidenceDigest
	sources   map[releasepolicy.Arm][]source
}

type Current struct {
	canonical []byte
	seal      models.EvidenceDigest
}

// Prepare captures both actual arms. No policy/schema/version contract or
// supplied request/digest is accepted or manufactured here.
func Prepare(ctx context.Context, locations map[releasepolicy.Arm]ArmLocation) (*Prepared, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(locations) != 2 {
		return nil, fmt.Errorf("native snapshot requires exactly baseline and candidate")
	}
	bindings := make(map[releasepolicy.Arm]boundLocation, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		location, ok := locations[arm]
		if !ok {
			return nil, fmt.Errorf("native snapshot is missing %s arm", arm)
		}
		binding, err := bind(ctx, location)
		if err != nil {
			return nil, fmt.Errorf("%s location: %w", arm, err)
		}
		bindings[arm] = binding
	}
	canonical, sources, err := recapture(ctx, bindings)
	if err != nil {
		return nil, err
	}
	digest, err := evidence.JSONDigest(json.RawMessage(canonical))
	if err != nil {
		return nil, err
	}
	return &Prepared{bindings, bytes.Clone(canonical), *digest, sources}, nil
}

// Recheck has no replacement-location argument: the caller cannot retarget an
// ID/path to a different root. It recaptures raw sources, discoveries, native
// declarations and ordered requests independently, not just metadata/hashes.
func Recheck(ctx context.Context, prepared *Prepared) (*Current, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared == nil || len(prepared.bindings) != 2 {
		return nil, fmt.Errorf("missing native preparation")
	}
	seal, err := evidence.JSONDigest(json.RawMessage(prepared.canonical))
	if err != nil || *seal != prepared.seal {
		return nil, fmt.Errorf("invalid detached native preparation")
	}
	canonical, sources, err := recapture(ctx, prepared.bindings)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, prepared.canonical) {
		return nil, fmt.Errorf("native source/configuration/request snapshot changed")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		before, after := prepared.sources[arm], sources[arm]
		if len(before) != len(after) {
			return nil, fmt.Errorf("native source inventory changed")
		}
		for i := range before {
			if before[i].Path != after[i].Path || !bytes.Equal(before[i].Bytes, after[i].Bytes) {
				return nil, fmt.Errorf("native raw source changed")
			}
		}
	}
	return &Current{bytes.Clone(canonical), *seal}, nil
}

// Request returns a fresh detached CLIENT request for inspection, not execution
// authorization. Disabled declarations are captured but have no request.
func (p *Prepared) Request(arm releasepolicy.Arm, taskID string) (*execution.ExecutionRequest, error) {
	if p == nil {
		return nil, fmt.Errorf("missing native preparation")
	}
	seal, err := evidence.JSONDigest(json.RawMessage(p.canonical))
	if err != nil || *seal != p.seal {
		return nil, fmt.Errorf("invalid detached native preparation")
	}
	var snapshots map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(p.canonical, &snapshots); err != nil {
		return nil, err
	}
	for _, task := range snapshots[arm].Tasks {
		if task.ID == taskID && task.Enabled {
			var request execution.ExecutionRequest
			decoder := json.NewDecoder(bytes.NewReader(task.Request))
			decoder.UseNumber()
			if err := decoder.Decode(&request); err != nil {
				return nil, err
			}
			return &request, nil
		}
	}
	return nil, fmt.Errorf("native selected task %q is unavailable", taskID)
}

func bind(ctx context.Context, location ArmLocation) (boundLocation, error) {
	if location.Root == nil || !filepath.IsAbs(location.Root.Name()) ||
		filepath.Clean(location.Root.Name()) != location.Root.Name() {
		return boundLocation{}, fmt.Errorf("native root requires a canonical absolute association")
	}
	base := location.Root.Name()
	// The trusted original path association must itself contain no symlinks.
	for path := base; ; path = filepath.Dir(path) {
		if err := ctx.Err(); err != nil {
			return boundLocation{}, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return boundLocation{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return boundLocation{}, fmt.Errorf("unsupported root path association")
		}
		if path == filepath.Dir(path) {
			break
		}
	}
	info, err := location.Root.Stat(".")
	if err != nil {
		return boundLocation{}, err
	}
	named, err := os.Stat(base)
	if err != nil || !os.SameFile(info, named) {
		return boundLocation{}, fmt.Errorf("root handle does not match original association")
	}
	if !filepath.IsAbs(location.CWD) || filepath.Clean(location.CWD) != location.CWD {
		return boundLocation{}, fmt.Errorf("native cwd requires a canonical absolute association")
	}
	cwdRel, err := filepath.Rel(base, location.CWD)
	if err != nil || canonical(cwdRel, true) != nil {
		return boundLocation{}, fmt.Errorf("unsupported cwd outside native root")
	}
	if err := canonical(location.EvalPath, false); err != nil {
		return boundLocation{}, err
	}
	if err := canonical(location.Executable, false); err != nil {
		return boundLocation{}, err
	}
	if location.ContextDir != "" {
		if filepath.IsAbs(location.ContextDir) {
			if filepath.Clean(location.ContextDir) != location.ContextDir {
				return boundLocation{}, fmt.Errorf("unsupported noncanonical absolute context")
			}
			rel, err := filepath.Rel(base, location.ContextDir)
			if err != nil || canonical(rel, true) != nil {
				return boundLocation{}, fmt.Errorf("unsupported absolute context outside native root")
			}
		} else if err := canonical(location.ContextDir, true); err != nil {
			return boundLocation{}, err
		}
	}
	files := &capturedFiles{ctx: ctx, root: location.Root, base: base}
	cwd, err := files.check(cwdRel)
	if err != nil {
		return boundLocation{}, err
	}
	if !cwd.IsDir() {
		return boundLocation{}, fmt.Errorf("native cwd is not a directory")
	}
	return boundLocation{location, base, info, cwd}, nil
}

func recapture(ctx context.Context, bindings map[releasepolicy.Arm]boundLocation) ([]byte, map[releasepolicy.Arm][]source, error) {
	snapshots := make(map[releasepolicy.Arm]armSnapshot, 2)
	sources := make(map[releasepolicy.Arm][]source, 2)
	total := 0
	taskCount := 0
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		binding := bindings[arm]
		current, err := bind(ctx, binding.location)
		if err != nil {
			return nil, nil, fmt.Errorf("%s root/cwd association changed: %w", arm, err)
		}
		if current.base != binding.base || !os.SameFile(binding.identity, current.identity) ||
			!os.SameFile(binding.cwdIdentity, current.cwdIdentity) {
			return nil, nil, fmt.Errorf("%s root/cwd association changed", arm)
		}
		snapshot, inventory, err := captureArm(ctx, binding, &total)
		if err != nil {
			return nil, nil, fmt.Errorf("%s native capture: %w", arm, err)
		}
		current, err = bind(ctx, binding.location)
		if err != nil {
			return nil, nil, fmt.Errorf("%s root/cwd association changed during capture: %w", arm, err)
		}
		if !os.SameFile(binding.identity, current.identity) ||
			!os.SameFile(binding.cwdIdentity, current.cwdIdentity) {
			return nil, nil, fmt.Errorf("%s root/cwd association changed during capture", arm)
		}
		snapshots[arm], sources[arm] = snapshot, inventory
		taskCount += len(snapshot.Tasks)
		if taskCount > maxTasks {
			return nil, nil, fmt.Errorf("two-arm native task limit exceeded")
		}
	}
	canonical, err := json.Marshal(snapshots)
	if err != nil {
		return nil, nil, err
	}
	if len(canonical) > maxProjectionBytes {
		return nil, nil, fmt.Errorf("native projection exceeds bounded limit")
	}
	return canonical, sources, nil
}

func captureArm(ctx context.Context, binding boundLocation, total *int) (armSnapshot, []source, error) {
	location := binding.location
	files := &capturedFiles{ctx: ctx, root: location.Root, base: binding.base,
		data: map[string][]byte{}, info: map[string]os.FileInfo{},
		dirs: map[string][]fs.DirEntry{}, roles: map[string]map[string]bool{}, total: total}
	result := armSnapshot{EvalPath: location.EvalPath, CWD: location.CWD, ContextDir: location.ContextDir,
		Executable: location.Executable, RootPath: binding.base, Discovery: []discovery{}, Tasks: []taskSnapshot{}}
	cwd, err := files.Stat(location.CWD)
	if err != nil {
		return result, nil, fmt.Errorf("checking associated native cwd: %w", err)
	}
	if !cwd.IsDir() {
		return result, nil, fmt.Errorf("native cwd is not an associated directory")
	}
	read := func(path, role string) ([]byte, error) {
		files.role = role
		return files.ReadFile(filepath.Join(binding.base, path))
	}
	eval, err := read(location.EvalPath, "eval")
	if err != nil {
		return result, nil, err
	}
	evalPath := filepath.Join(binding.base, location.EvalPath)
	spec, err := models.ParseEvalSpecOffline(eval, evalPath)
	if err != nil {
		return result, nil, err
	}
	result.Native, err = json.Marshal(spec)
	if err != nil {
		return result, nil, err
	}
	projectionSize := len(result.Native)
	if projectionSize > maxProjectionBytes {
		return result, nil, fmt.Errorf("native declaration projection exceeds bounded limit")
	}
	for _, path := range spec.Config.InstructionFiles {
		if err := canonical(path, false); err != nil {
			return result, nil, err
		}
	}
	specDir := filepath.Dir(evalPath)
	lockPath := filepath.Join(filepath.Dir(location.EvalPath), models.LockfileName)
	_, err = files.check(lockPath)
	if err == nil {
		if _, err := read(lockPath, "present_lock"); err != nil {
			return result, nil, err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return result, nil, err
	}
	if _, err := read(location.Executable, "executable"); err != nil {
		return result, nil, err
	}
	ids := map[string]bool{}
	enabled := 0
	for _, pattern := range spec.Tasks {
		if err := canonical(pattern, false); err != nil {
			return result, nil, err
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			return result, nil, err
		}
		rootPattern := filepath.Join(filepath.Dir(location.EvalPath), pattern)
		matches, err := fs.Glob(discoveryFiles{files}, rootPattern)
		if err != nil {
			return result, nil, err
		}
		if files.sticky != nil {
			return result, nil, files.sticky
		}
		result.Discovery = append(result.Discovery, discovery{pattern, append([]string{}, matches...)})
		for _, path := range matches {
			if len(result.Tasks) >= maxTasks {
				return result, nil, fmt.Errorf("native task limit exceeded")
			}
			taskBytes, err := read(path, "task")
			if err != nil {
				return result, nil, err
			}
			files.role = "prompt_file"
			task, err := models.ParseTestCaseOffline(taskBytes, filepath.Join(binding.base, path), func(promptPath string) ([]byte, error) {
				rel, err := filepath.Rel(filepath.Dir(filepath.Join(binding.base, path)), promptPath)
				if err != nil || canonical(rel, false) != nil {
					return nil, fmt.Errorf("unsupported prompt_file path")
				}
				return files.ReadFile(promptPath)
			})
			if err != nil {
				return result, nil, err
			}
			if ids[task.TestID] || task.TestID == "" {
				return result, nil, fmt.Errorf("duplicate/empty native task ID, including disabled declarations")
			}
			ids[task.TestID] = true
			if err := orchestration.ValidateNativeSnapshotConfiguration(spec, task); err != nil {
				return result, nil, err
			}
			for _, input := range task.InstructionFiles {
				if err := canonical(input, false); err != nil {
					return result, nil, err
				}
			}
			for _, ref := range task.Stimulus.Resources {
				if err := canonical(ref.Location, false); err != nil {
					return result, nil, err
				}
			}
			if value, ok := task.Stimulus.Metadata["fixture"]; ok {
				if path, ok := value.(string); ok {
					if err := canonical(path, false); err != nil {
						return result, nil, err
					}
				}
			}
			native, err := json.Marshal(task)
			if err != nil {
				return result, nil, err
			}
			projectionSize += len(native)
			if projectionSize > maxProjectionBytes {
				return result, nil, fmt.Errorf("native task projection exceeds bounded limit")
			}
			selected := task.Active == nil || *task.Active
			row := taskSnapshot{Path: path, ID: task.TestID, Enabled: selected, Native: native}
			if selected {
				enabled++
				contextDir := location.ContextDir
				if contextDir == "" {
					contextDir = filepath.Join(specDir, "fixtures")
				} else if !filepath.IsAbs(contextDir) {
					contextDir = filepath.Join(location.CWD, contextDir)
				}
				if task.ContextRoot != "" {
					if filepath.IsAbs(task.ContextRoot) && filepath.Clean(task.ContextRoot) != task.ContextRoot {
						return result, nil, fmt.Errorf("unsupported noncanonical absolute task context")
					}
					if err := canonical(task.ContextRoot, true); err != nil && !filepath.IsAbs(task.ContextRoot) {
						return result, nil, err
					}
					if !filepath.IsAbs(task.ContextRoot) {
						task.ContextRoot = filepath.Join(location.CWD, task.ContextRoot)
					}
					if _, err := files.relative(task.ContextRoot); err != nil {
						return result, nil, err
					}
				}
				files.role = "request_input"
				// First resolve the native read dependencies using the same
				// constructor; then build the retained request from inert bytes.
				_, err := orchestration.BuildCapturedNativeRequest(spec, task, specDir, contextDir, files)
				if err != nil {
					return result, nil, err
				}
				if files.sticky != nil {
					return result, nil, fmt.Errorf("missing/unsupported native request input: %w", files.sticky)
				}
				files.frozen = true
				request, err := orchestration.BuildCapturedNativeRequest(spec, task, specDir, contextDir, files)
				files.frozen = false
				if err != nil {
					return result, nil, err
				}
				if err := validateRequest(request); err != nil {
					return result, nil, err
				}
				inputSize := len(request.Message)
				for _, input := range request.Resources {
					inputSize += len(input.Content)
				}
				for _, input := range request.Instructions {
					inputSize += len(input.Content)
				}
				if inputSize > maxProjectionBytes-projectionSize {
					return result, nil, fmt.Errorf("native request inputs exceed bounded projection")
				}
				row.Request, err = orchestration.FreezeCapturedNativeRequest(request)
				if err != nil {
					return result, nil, err
				}
				projectionSize += len(row.Request)
				if projectionSize > maxProjectionBytes {
					return result, nil, fmt.Errorf("native request projection exceeds bounded limit")
				}
			}
			result.Tasks = append(result.Tasks, row)
		}
	}
	if enabled == 0 {
		return result, nil, fmt.Errorf("native arm requires an actual enabled task")
	}
	if err := files.verifyStable(); err != nil {
		return result, nil, err
	}
	inventory := files.inventory()
	result.Sources = make([]sourceIdentity, 0, len(inventory))
	for _, source := range inventory {
		result.Sources = append(result.Sources, sourceIdentity{source.Path, source.Roles, len(source.Bytes), source.Digest})
	}
	return result, inventory, nil
}

func validateRequest(req *execution.ExecutionRequest) error {
	model := strings.TrimSpace(req.ModelID)
	if model == "" || model != req.ModelID || strings.EqualFold(model, "auto") || strings.EqualFold(model, "unknown") {
		return fmt.Errorf("unsupported native model identity")
	}
	if req.Message == "" || req.FirstEventTimeout < 0 {
		return fmt.Errorf("unsupported native empty prompt/timeout")
	}
	if req.WorkDir != "" {
		if err := canonical(req.WorkDir, false); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, input := range req.Resources {
		if err := canonical(input.Path, false); err != nil {
			return err
		}
		if seen[input.Path] {
			return fmt.Errorf("unsupported duplicated native resource destination")
		}
		seen[input.Path] = true
	}
	seen = map[string]bool{}
	for _, input := range req.Instructions {
		if err := canonical(input.Path, false); err != nil {
			return err
		}
		if seen[input.Path] {
			return fmt.Errorf("unsupported duplicated native instruction destination")
		}
		seen[input.Path] = true
	}
	return nil
}
