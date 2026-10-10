package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/controlledprojection"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
)

type retainedArmInputs struct {
	ctx             context.Context
	base            string
	associated      armAssociations
	sources         []source
	phase           associationPhase
	scope           uint32
	position        int
	role            string
	sticky          error
	nodes           map[string]uint32
	directories     map[string]uint32
	usedNodes       []bool
	usedDirectories []bool
}

func newRetainedArmInputs(ctx context.Context, base string, associated armAssociations, sources []source) *retainedArmInputs {
	reader := &retainedArmInputs{ctx: ctx, base: base, associated: associated, sources: sources,
		nodes: map[string]uint32{}, directories: map[string]uint32{},
		usedNodes: make([]bool, len(associated.Nodes)), usedDirectories: make([]bool, len(associated.Directories))}
	for i, node := range associated.Nodes {
		reader.nodes[node.Path] = uint32(i)
	}
	for i, directory := range associated.Directories {
		reader.directories[directory.Path] = uint32(i)
	}
	return reader
}

func (r *retainedArmInputs) set(phase associationPhase, scope uint32, role string) {
	r.phase, r.scope, r.role = phase, scope, role
}
func (r *retainedArmInputs) SetInputRole(role string) { r.role = role }

func (r *retainedArmInputs) fail(err error) error {
	if err != nil && r.sticky == nil {
		r.sticky = err
	}
	return err
}

func (r *retainedArmInputs) label(absolute string) (string, error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return "", r.fail(fmt.Errorf("noncanonical retained native path"))
	}
	label, err := filepath.Rel(r.base, absolute)
	if err == nil {
		err = associationPath(label)
	}
	return label, r.fail(err)
}

func (r *retainedArmInputs) next(label string, op associationOperation) (associationEvent, error) {
	if err := r.ctx.Err(); err != nil {
		return associationEvent{}, r.fail(err)
	}
	if r.sticky != nil {
		return associationEvent{}, r.sticky
	}
	if r.position >= len(r.associated.Events) {
		return associationEvent{}, r.fail(fmt.Errorf("native association tape exhausted"))
	}
	event := r.associated.Events[r.position]
	if event.Phase != r.phase || event.ScopeIndex != r.scope || event.Role != r.role || event.Op != op || event.Path != label {
		return associationEvent{}, r.fail(fmt.Errorf("native association consumer mismatch at ordinal %d", event.Ordinal))
	}
	r.position++
	return event, nil
}

func (r *retainedArmInputs) read(label string) ([]byte, error) {
	event, err := r.next(label, readOperation)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(r.sources[*event.Source].Bytes), nil
}

func (r *retainedArmInputs) ReadFile(absolute string) ([]byte, error) {
	label, err := r.label(absolute)
	if err != nil {
		return nil, err
	}
	return r.read(label)
}

func (r *retainedArmInputs) kind(label string, op associationOperation) (nodeKind, error) {
	event, err := r.next(label, op)
	if err != nil {
		return "", err
	}
	node := r.associated.Nodes[*event.Node]
	r.usedNodes[*event.Node] = true
	if node.State == missingNode {
		return "", fs.ErrNotExist
	}
	return *node.Kind, nil
}

func (r *retainedArmInputs) Kind(absolute string) (orchestration.RequestInputKind, error) {
	label, err := r.label(absolute)
	if err != nil {
		return "", err
	}
	kind, err := r.kind(label, kindOperation)
	if err != nil {
		return "", r.fail(err)
	}
	return orchestration.RequestInputKind(kind), nil
}

func (r *retainedArmInputs) Resolve(absolute string) (string, error) {
	label, err := r.label(absolute)
	if err != nil {
		return "", err
	}
	if _, err := r.kind(label, resolveOperation); err != nil {
		return "", r.fail(err)
	}
	return absolute, nil
}

func (r *retainedArmInputs) Walk(absolute string, visit func(string, orchestration.RequestInputKind) error) error {
	label, err := r.label(absolute)
	if err != nil {
		return err
	}
	if _, err := r.next(label, walkBeginOperation); err != nil {
		return err
	}
	var walk func(string, int) error
	walk = func(current string, depth int) error {
		if depth > 10000 {
			return r.fail(fmt.Errorf("native retained walk exceeds bounded depth"))
		}
		kind, err := r.kind(current, walkEntryOperation)
		if err != nil {
			return r.fail(err)
		}
		if err := visit(filepath.Join(r.base, current), orchestration.RequestInputKind(kind)); err != nil {
			return r.fail(err)
		}
		if kind != directoryNode {
			return nil
		}
		index, ok := r.directories[current]
		if !ok {
			return r.fail(fmt.Errorf("missing captured native walk directory, including empty directories"))
		}
		r.usedDirectories[index] = true
		for _, entry := range r.associated.Directories[index].Entries {
			if err := walk(path.Join(current, entry.Name), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(label, 0); err != nil {
		return err
	}
	_, err = r.next(label, walkEndOperation)
	return err
}

type retainedDiscoveryInputs struct{ reader *retainedArmInputs }

func (r *retainedDiscoveryInputs) Kind(label string) (nodeKind, error) {
	return r.reader.kind(label, kindOperation)
}

func (r *retainedDiscoveryInputs) DirectoryNames(label string) ([]string, error) {
	event, err := r.reader.next(label, directoryOperation)
	if err != nil {
		return nil, err
	}
	index := *event.Directory
	r.reader.usedDirectories[index] = true
	node, ok := r.reader.nodes[label]
	if !ok {
		return nil, r.reader.fail(fmt.Errorf("missing native discovery directory node"))
	}
	r.reader.usedNodes[node] = true
	entries := r.reader.associated.Directories[index].Entries
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := r.reader.ctx.Err(); err != nil {
			return nil, r.reader.fail(err)
		}
		names = append(names, entry.Name)
	}
	return names, nil
}

// Discovery still uses fs.Glob at capture time. This pure matcher consumes only
// its retained typed query observations, in the same query and result order.
func matchCapturedPattern(ctx context.Context, pattern string, tree *retainedDiscoveryInputs) ([]string, error) {
	if ctx == nil || tree == nil || tree.reader == nil {
		return nil, fmt.Errorf("missing retained discovery inputs")
	}
	var glob func(string, int) ([]string, error)
	directory := func(dir, pattern string, matches []string) ([]string, error) {
		names, err := tree.DirectoryNames(dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			matched, err := path.Match(pattern, name)
			if err != nil {
				return nil, err
			}
			if matched {
				if len(matches) >= maxEntries {
					return nil, fmt.Errorf("native retained discovery exceeds bounded matches")
				}
				matches = append(matches, path.Join(dir, name))
			}
		}
		return matches, nil
	}
	glob = func(pattern string, depth int) ([]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if depth > 10000 {
			return nil, path.ErrBadPattern
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, err
		}
		if !strings.ContainsAny(pattern, "*?[\\") {
			if _, err := tree.Kind(pattern); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil, nil
				}
				return nil, err
			}
			return []string{pattern}, nil
		}
		dir, file := path.Split(pattern)
		if dir == "" {
			dir = "."
		} else {
			dir = dir[:len(dir)-1]
		}
		if !strings.ContainsAny(dir, "*?[\\") {
			return directory(dir, file, nil)
		}
		if dir == pattern {
			return nil, path.ErrBadPattern
		}
		parents, err := glob(dir, depth+1)
		if err != nil {
			return nil, err
		}
		var matches []string
		for _, parent := range parents {
			matches, err = directory(parent, file, matches)
			if err != nil {
				return nil, err
			}
		}
		return matches, nil
	}
	return glob(pattern, 0)
}

func sameNativeJSON(actual any, expected json.RawMessage) error {
	raw, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, expected) {
		return fmt.Errorf("native pre-context declaration bytes changed")
	}
	actualDigest, err := evidence.JSONDigest(json.RawMessage(raw))
	if err != nil {
		return err
	}
	expectedDigest, err := evidence.JSONDigest(expected)
	if err != nil {
		return err
	}
	if *actualDigest != *expectedDigest {
		return fmt.Errorf("native declaration JSON-v1 changed")
	}
	return nil
}

func reconstructProjection(ctx context.Context, input detachedProjectionInput) (map[releasepolicy.Arm]controlledprojection.SourceProjection, error) {
	validated, err := validateProjectionInput(ctx, input)
	if err != nil {
		return nil, err
	}
	associated, err := decodeAssociations(ctx, validated.associations.canonical)
	if err != nil {
		return nil, err
	}
	var snapshots map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(validated.canonical, &snapshots); err != nil {
		return nil, err
	}
	result := make(map[releasepolicy.Arm]controlledprojection.SourceProjection, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		projection, err := reconstructArm(ctx, snapshots[arm], associated.Arms[arm], validated.sources[arm])
		if err != nil {
			return nil, fmt.Errorf("%s native reconstruction: %w", arm, err)
		}
		result[arm] = projection
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func reconstructArm(ctx context.Context, snapshot armSnapshot, associated armAssociations, sources []source) (controlledprojection.SourceProjection, error) {
	reader := newRetainedArmInputs(ctx, snapshot.RootPath, associated, sources)
	reader.set(evalPhase, 0, "eval")
	evalBytes, err := reader.read(snapshot.EvalPath)
	if err != nil {
		return controlledprojection.SourceProjection{}, err
	}
	evalPath := filepath.Join(snapshot.RootPath, snapshot.EvalPath)
	spec, err := models.ParseEvalSpecOffline(evalBytes, evalPath)
	if err != nil {
		return controlledprojection.SourceProjection{}, err
	}
	if err := sameNativeJSON(spec, snapshot.Native); err != nil {
		return controlledprojection.SourceProjection{}, err
	}
	suite := controlledprojection.SuiteInput{Spec: *spec, Tasks: []controlledprojection.TaskInput{}}
	reader.set(lockPhase, 0, "present_lock")
	lockPath := path.Join(path.Dir(snapshot.EvalPath), models.LockfileName)
	kind, err := reader.kind(lockPath, kindOperation)
	if err == nil {
		if kind != regularNode {
			return controlledprojection.SourceProjection{}, fmt.Errorf("native lock is not regular")
		}
		suite.LockPresent = true
		suite.LockBytes, err = reader.read(lockPath)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return controlledprojection.SourceProjection{}, err
	}
	reader.set(executablePhase, 0, "executable")
	suite.Executable, err = reader.read(snapshot.Executable)
	if err != nil {
		return controlledprojection.SourceProjection{}, err
	}
	if len(spec.Tasks) != len(snapshot.Discovery) {
		return controlledprojection.SourceProjection{}, fmt.Errorf("native discovery inventory changed")
	}
	taskIndex := 0
	ids := map[string]bool{}
	for patternIndex, pattern := range spec.Tasks {
		if err := canonical(pattern, false); err != nil {
			return controlledprojection.SourceProjection{}, err
		}
		if snapshot.Discovery[patternIndex].Pattern != pattern {
			return controlledprojection.SourceProjection{}, fmt.Errorf("native discovery pattern changed")
		}
		reader.set(discoveryPhase, uint32(patternIndex), "task")
		matches, err := matchCapturedPattern(ctx, path.Join(path.Dir(snapshot.EvalPath), pattern), &retainedDiscoveryInputs{reader})
		if err != nil {
			return controlledprojection.SourceProjection{}, err
		}
		if !slices.Equal(matches, snapshot.Discovery[patternIndex].Paths) {
			return controlledprojection.SourceProjection{}, fmt.Errorf("native discovery results changed")
		}
		for _, label := range matches {
			if taskIndex >= len(snapshot.Tasks) {
				return controlledprojection.SourceProjection{}, fmt.Errorf("native task inventory incomplete")
			}
			row := snapshot.Tasks[taskIndex]
			if row.Path != label {
				return controlledprojection.SourceProjection{}, fmt.Errorf("native task path/order changed")
			}
			reader.set(taskPhase, uint32(taskIndex), "task")
			taskBytes, err := reader.read(label)
			if err != nil {
				return controlledprojection.SourceProjection{}, err
			}
			reader.set(promptPhase, uint32(taskIndex), "prompt_file")
			taskPath := filepath.Join(snapshot.RootPath, label)
			task, err := models.ParseTestCaseOffline(taskBytes, taskPath, func(absolute string) ([]byte, error) {
				relative, err := filepath.Rel(filepath.Dir(taskPath), absolute)
				if err != nil || canonical(relative, false) != nil {
					return nil, reader.fail(fmt.Errorf("unsupported prompt_file path"))
				}
				return reader.ReadFile(absolute)
			})
			if err != nil {
				return controlledprojection.SourceProjection{}, err
			}
			if err := sameNativeJSON(task, row.Native); err != nil {
				return controlledprojection.SourceProjection{}, err
			}
			selected := task.Active == nil || *task.Active
			if task.TestID == "" || ids[task.TestID] || task.TestID != row.ID || selected != row.Enabled {
				return controlledprojection.SourceProjection{}, fmt.Errorf("native task declaration ownership changed")
			}
			ids[task.TestID] = true
			if err := orchestration.ValidateNativeSnapshotConfiguration(spec, task); err != nil {
				return controlledprojection.SourceProjection{}, err
			}
			if selected {
				contextDir := snapshot.ContextDir
				if contextDir == "" {
					contextDir = filepath.Join(filepath.Dir(evalPath), "fixtures")
				} else if !filepath.IsAbs(contextDir) {
					contextDir = filepath.Join(snapshot.CWD, contextDir)
				}
				if task.ContextRoot != "" {
					if filepath.IsAbs(task.ContextRoot) && filepath.Clean(task.ContextRoot) != task.ContextRoot {
						return controlledprojection.SourceProjection{}, fmt.Errorf("unsupported noncanonical task context")
					}
					if !filepath.IsAbs(task.ContextRoot) {
						if err := canonical(task.ContextRoot, true); err != nil {
							return controlledprojection.SourceProjection{}, err
						}
						task.ContextRoot = filepath.Join(snapshot.CWD, task.ContextRoot)
					}
					if _, err := reader.label(task.ContextRoot); err != nil {
						return controlledprojection.SourceProjection{}, err
					}
				}
				reader.set(requestPhase, uint32(taskIndex), "request_input")
				request, err := orchestration.BuildRetainedNativeRequest(ctx, spec, task, filepath.Dir(evalPath), contextDir, reader)
				if err != nil {
					return controlledprojection.SourceProjection{}, err
				}
				if reader.sticky != nil {
					return controlledprojection.SourceProjection{}, reader.sticky
				}
				if err := validateRequest(request); err != nil {
					return controlledprojection.SourceProjection{}, err
				}
				raw, err := orchestration.FreezeCapturedNativeRequest(request)
				if err != nil {
					return controlledprojection.SourceProjection{}, err
				}
				if !bytes.Equal(raw, row.Request) {
					return controlledprojection.SourceProjection{}, fmt.Errorf("complete native CLIENT bytes changed")
				}
				gotDigest, err := evidence.JSONDigest(json.RawMessage(raw))
				if err != nil {
					return controlledprojection.SourceProjection{}, err
				}
				oldDigest, err := evidence.JSONDigest(row.Request)
				if err != nil {
					return controlledprojection.SourceProjection{}, err
				}
				if *gotDigest != *oldDigest {
					return controlledprojection.SourceProjection{}, fmt.Errorf("complete native CLIENT JSON-v1 changed")
				}
				suite.Tasks = append(suite.Tasks, controlledprojection.TaskInput{Definition: *task, Request: *request})
			} else if len(row.Request) != 0 && !bytes.Equal(row.Request, []byte("null")) {
				return controlledprojection.SourceProjection{}, fmt.Errorf("disabled native task has CLIENT bytes")
			}
			taskIndex++
		}
	}
	if taskIndex != len(snapshot.Tasks) || reader.position != len(reader.associated.Events) || reader.sticky != nil {
		return controlledprojection.SourceProjection{}, fmt.Errorf("native reconstruction did not exhaust task/event inventory")
	}
	for _, used := range reader.usedNodes {
		if !used {
			return controlledprojection.SourceProjection{}, fmt.Errorf("native reconstruction did not exhaust node inventory")
		}
	}
	for _, used := range reader.usedDirectories {
		if !used {
			return controlledprojection.SourceProjection{}, fmt.Errorf("native reconstruction did not exhaust directory inventory")
		}
	}
	if err := ctx.Err(); err != nil {
		return controlledprojection.SourceProjection{}, err
	}
	return controlledprojection.NativePlan(suite)
}
