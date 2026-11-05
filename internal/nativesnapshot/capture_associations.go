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
	"sort"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/orchestration"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const associationKind = "waza.internal.native-capture-associations"
const associationVersion = "1"

const (
	maxAssociationBytes       = 16 << 20
	maxAssociationEvents      = 65536
	maxAssociationNodes       = 32768
	maxAssociationDirectories = 16384
	maxAssociationPath        = 4096
)

type nodeKind string

const (
	regularNode   nodeKind = "regular"
	directoryNode nodeKind = "directory"
)

type nodeState string

const (
	presentNode nodeState = "present"
	missingNode nodeState = "missing"
)

type capturedNode struct {
	Path  string    `json:"path"`
	State nodeState `json:"state"`
	Kind  *nodeKind `json:"kind,omitempty"`
}

type capturedChild struct {
	Name string `json:"name"`
}

type capturedDirectory struct {
	Path    string          `json:"path"`
	Entries []capturedChild `json:"entries"`
}

type associationPhase string

const (
	evalPhase       associationPhase = "eval"
	lockPhase       associationPhase = "lock"
	executablePhase associationPhase = "executable"
	discoveryPhase  associationPhase = "discovery"
	taskPhase       associationPhase = "task"
	promptPhase     associationPhase = "prompt"
	requestPhase    associationPhase = "request"
)

type associationOperation string

const (
	readOperation      associationOperation = "read"
	kindOperation      associationOperation = "kind"
	resolveOperation   associationOperation = "resolve"
	directoryOperation associationOperation = "directory"
	walkBeginOperation associationOperation = "walk_begin"
	walkEntryOperation associationOperation = "walk_entry"
	walkEndOperation   associationOperation = "walk_end"
)

type associationEvent struct {
	Ordinal    uint32               `json:"ordinal"`
	Phase      associationPhase     `json:"phase"`
	ScopeIndex uint32               `json:"scope_index"`
	Role       string               `json:"role"`
	Op         associationOperation `json:"op"`
	Path       string               `json:"path"`
	Node       *uint32              `json:"node,omitempty"`
	Source     *uint32              `json:"source,omitempty"`
	Directory  *uint32              `json:"directory,omitempty"`
}

type armAssociations struct {
	Nodes       []capturedNode      `json:"nodes"`
	Directories []capturedDirectory `json:"directories"`
	Events      []associationEvent  `json:"events"`
}

type captureAssociations struct {
	Kind     string                                `json:"kind"`
	Version  string                                `json:"version"`
	Snapshot models.EvidenceDigest                 `json:"snapshot"`
	Arms     map[releasepolicy.Arm]armAssociations `json:"arms"`
}

type retainedAssociations struct {
	canonical []byte
	seal      models.EvidenceDigest
}

type projectionCapture struct {
	prepared     *Prepared
	associations retainedAssociations
}

type detachedProjectionInput struct {
	canonical    []byte
	snapshot     models.EvidenceDigest
	sources      map[releasepolicy.Arm][]source
	associations retainedAssociations
}

// The budget is shared across arms and charged before retaining observations.
type associationBudget struct{ events, nodes, directories, bytes int }

func (b *associationBudget) charge(counter *int, limit, size int) error {
	if *counter >= limit || size > maxAssociationBytes-b.bytes {
		return fmt.Errorf("native capture association limit exceeded")
	}
	*counter++
	b.bytes += size
	return nil
}

type associationRecorder struct {
	ctx         context.Context
	base        string
	budget      *associationBudget
	phase       associationPhase
	scope       uint32
	nodes       map[string]capturedNode
	directories map[string]capturedDirectory
	events      []associationEvent
	err         error
}

func newAssociationRecorder(ctx context.Context, base string, budget *associationBudget) *associationRecorder {
	return &associationRecorder{ctx: ctx, base: base, budget: budget,
		nodes: map[string]capturedNode{}, directories: map[string]capturedDirectory{}, events: []associationEvent{}}
}

func (r *associationRecorder) set(phase associationPhase, scope uint32) {
	r.phase, r.scope = phase, scope
}

func associationPath(path string) error {
	if len(path) > maxAssociationPath {
		return fmt.Errorf("native association path exceeds bounded limit")
	}
	return canonical(path, true)
}

func (r *associationRecorder) label(path string) (string, error) {
	label, err := filepath.Rel(r.base, path)
	if err != nil {
		return "", err
	}
	return label, associationPath(label)
}

func (r *associationRecorder) addNode(path string, kind nodeKind, missing bool) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if err := associationPath(path); err != nil {
		return err
	}
	state := presentNode
	if missing {
		state = missingNode
	} else if kind != regularNode && kind != directoryNode {
		return fmt.Errorf("unsupported native association kind")
	}
	if old, ok := r.nodes[path]; ok {
		if old.State != state || (old.Kind != nil && (missing || *old.Kind != kind)) {
			return fmt.Errorf("native observed node changed")
		}
		return nil
	}
	if err := r.budget.charge(&r.budget.nodes, maxAssociationNodes, len(path)+16); err != nil {
		return err
	}
	node := capturedNode{Path: path, State: state}
	if !missing {
		node.Kind = &kind
	}
	r.nodes[path] = node
	return nil
}

func (r *associationRecorder) event(path, role string, op associationOperation) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if err := associationPath(path); err != nil {
		return err
	}
	if len(role) > maxAssociationPath {
		return fmt.Errorf("native association role exceeds bounded limit")
	}
	if err := r.budget.charge(&r.budget.events, maxAssociationEvents, len(path)+len(role)+32); err != nil {
		return err
	}
	r.events = append(r.events, associationEvent{Ordinal: uint32(len(r.events) + 1),
		Phase: r.phase, ScopeIndex: r.scope, Role: role, Op: op, Path: path})
	return nil
}

func (r *associationRecorder) read(path, role string) error {
	return r.event(path, role, readOperation)
}

func (r *associationRecorder) observe(path, role string, info os.FileInfo, err error) error {
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	var kind nodeKind
	if !missing {
		switch {
		case info.IsDir():
			kind = directoryNode
		case info.Mode().IsRegular():
			kind = regularNode
		default:
			return fmt.Errorf("unsupported observed native node")
		}
	}
	if err := r.addNode(path, kind, missing); err != nil {
		return err
	}
	return r.event(path, role, kindOperation)
}

func (r *associationRecorder) directory(path string, entries []fs.DirEntry) error {
	if _, ok := r.directories[path]; ok {
		return nil
	}
	size := len(path) + 16
	for _, entry := range entries {
		name := entry.Name()
		if err := associationPath(name); err != nil {
			return err
		}
		if filepath.Base(name) != name || name == "." {
			return fmt.Errorf("unsupported native directory name")
		}
		if len(name)+8 > maxAssociationBytes-size {
			return fmt.Errorf("native directory association exceeds bounded limit")
		}
		size += len(name) + 8
	}
	if err := r.budget.charge(&r.budget.directories, maxAssociationDirectories, size); err != nil {
		return err
	}
	children := make([]capturedChild, 0, len(entries))
	for _, entry := range entries {
		children = append(children, capturedChild{Name: entry.Name()})
	}
	r.directories[path] = capturedDirectory{Path: path, Entries: children}
	return r.addNode(path, directoryNode, false)
}

func (r *associationRecorder) finish(inventory []source) (armAssociations, error) {
	if r.err != nil {
		return armAssociations{}, r.err
	}
	result := armAssociations{Nodes: []capturedNode{}, Directories: []capturedDirectory{}, Events: r.events}
	for _, node := range r.nodes {
		if err := r.ctx.Err(); err != nil {
			return armAssociations{}, err
		}
		result.Nodes = append(result.Nodes, node)
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Path < result.Nodes[j].Path })
	for _, directory := range r.directories {
		if err := r.ctx.Err(); err != nil {
			return armAssociations{}, err
		}
		result.Directories = append(result.Directories, directory)
	}
	sort.Slice(result.Directories, func(i, j int) bool { return result.Directories[i].Path < result.Directories[j].Path })
	nodes, dirs, sources := map[string]uint32{}, map[string]uint32{}, map[string]uint32{}
	for i, node := range result.Nodes {
		nodes[node.Path] = uint32(i)
	}
	for i, directory := range result.Directories {
		dirs[directory.Path] = uint32(i)
	}
	for i, source := range inventory {
		sources[source.Path] = uint32(i)
	}
	for i := range result.Events {
		if err := r.ctx.Err(); err != nil {
			return armAssociations{}, err
		}
		event := &result.Events[i]
		var index uint32
		var ok bool
		switch event.Op {
		case readOperation:
			index, ok = sources[event.Path]
			event.Source = &index
		case kindOperation, resolveOperation, walkEntryOperation:
			index, ok = nodes[event.Path]
			event.Node = &index
		case directoryOperation:
			index, ok = dirs[event.Path]
			event.Directory = &index
		default:
			ok = true
		}
		if !ok {
			return armAssociations{}, fmt.Errorf("native association inventory incomplete")
		}
	}
	return result, r.ctx.Err()
}

func (r *associationRecorder) recheckMissing(files *capturedFiles) error {
	for path, node := range r.nodes {
		if node.State != missingNode {
			continue
		}
		_, err := files.check(path)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("native missing observation changed: %s", path)
		}
	}
	return r.ctx.Err()
}

type associatedDiscoveryFiles struct {
	discoveryFiles
	recorder *associationRecorder
}

func (f associatedDiscoveryFiles) Stat(path string) (os.FileInfo, error) {
	info, err := f.discoveryFiles.Stat(path)
	if recordErr := f.recorder.observe(path, "task", info, err); recordErr != nil {
		f.sticky = recordErr
		return nil, recordErr
	}
	return info, err
}

func (f associatedDiscoveryFiles) ReadDir(path string) ([]fs.DirEntry, error) {
	entries, err := f.discoveryFiles.ReadDir(path)
	if err == nil {
		err = f.recorder.directory(path, entries)
	}
	if err == nil {
		err = f.recorder.event(path, "task", directoryOperation)
	}
	if err != nil {
		f.sticky = err
		return nil, err
	}
	return entries, nil
}

type semanticRequestInputs struct {
	inputs   orchestration.RequestInputs
	files    *capturedFiles
	recorder *associationRecorder
	role     string
}

func (f *semanticRequestInputs) SetInputRole(role string) {
	f.role = role
	if target, ok := f.inputs.(interface{ SetInputRole(string) }); ok {
		target.SetInputRole(role)
	}
}

func (f *semanticRequestInputs) retain(err error) error {
	if err != nil && f.recorder.err == nil {
		f.recorder.err = err
	}
	return err
}

func (f *semanticRequestInputs) ReadFile(path string) ([]byte, error) {
	data, err := f.inputs.ReadFile(path)
	if err != nil {
		return nil, f.retain(err)
	}
	label, err := f.recorder.label(path)
	if err == nil {
		err = f.recorder.read(label, f.role)
	}
	if err != nil {
		return nil, f.retain(err)
	}
	return data, nil
}

func (f *semanticRequestInputs) observe(path string, kind orchestration.RequestInputKind, op associationOperation) error {
	label, err := f.recorder.label(path)
	if err == nil {
		err = f.recorder.addNode(label, nodeKind(kind), false)
	}
	if err == nil && kind == orchestration.RequestInputDirectory {
		if entries, ok := f.files.dirs[label]; ok {
			err = f.recorder.directory(label, entries)
		}
	}
	if err == nil {
		err = f.recorder.event(label, f.role, op)
	}
	return f.retain(err)
}

func (f *semanticRequestInputs) Kind(path string) (orchestration.RequestInputKind, error) {
	kind, err := f.inputs.Kind(path)
	if err == nil {
		err = f.observe(path, kind, kindOperation)
	}
	return kind, f.retain(err)
}

func (f *semanticRequestInputs) Resolve(path string) (string, error) {
	resolved, err := f.inputs.Resolve(path)
	if err != nil {
		return "", f.retain(err)
	}
	info := f.files.infoMust(path)
	if info == nil {
		return "", f.retain(fmt.Errorf("uncaptured resolved native node"))
	}
	kind := orchestration.RequestInputRegular
	if info.IsDir() {
		kind = orchestration.RequestInputDirectory
	}
	if err := f.observe(path, kind, resolveOperation); err != nil {
		return "", err
	}
	return resolved, nil
}

func (f *capturedFiles) infoMust(path string) os.FileInfo {
	label, err := f.relative(path)
	if err != nil {
		return nil
	}
	return f.info[label]
}

func (f *semanticRequestInputs) Walk(path string, visit func(string, orchestration.RequestInputKind) error) error {
	label, err := f.recorder.label(path)
	if err == nil {
		err = f.recorder.event(label, f.role, walkBeginOperation)
	}
	if err != nil {
		return f.retain(err)
	}
	err = f.inputs.Walk(path, func(path string, kind orchestration.RequestInputKind) error {
		if err := f.observe(path, kind, walkEntryOperation); err != nil {
			return err
		}
		return visit(path, kind)
	})
	if err == nil {
		err = f.recorder.event(label, f.role, walkEndOperation)
	}
	return f.retain(err)
}

func captureArmWithAssociations(ctx context.Context, binding boundLocation, total *int) (armSnapshot, []source, armAssociations, error) {
	if ctx == nil || total == nil || binding.location.Root == nil {
		return armSnapshot{}, nil, armAssociations{}, fmt.Errorf("native associated capture requires context, bound root and byte counter")
	}
	if err := ctx.Err(); err != nil {
		return armSnapshot{}, nil, armAssociations{}, err
	}
	recorder := newAssociationRecorder(ctx, binding.base, &associationBudget{})
	snapshot, sources, err := captureArmRecorded(ctx, binding, total, recorder)
	if err != nil {
		return armSnapshot{}, nil, armAssociations{}, err
	}
	associations, err := recorder.finish(sources)
	if err != nil {
		return armSnapshot{}, nil, armAssociations{}, err
	}
	return snapshot, sources, associations, nil
}

func recaptureWithAssociations(ctx context.Context, bindings map[releasepolicy.Arm]boundLocation) ([]byte, map[releasepolicy.Arm][]source, map[releasepolicy.Arm]armAssociations, error) {
	if ctx == nil {
		return nil, nil, nil, fmt.Errorf("native associated recapture requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if len(bindings) != 2 {
		return nil, nil, nil, fmt.Errorf("native associated recapture requires exactly both arms")
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if _, ok := bindings[arm]; !ok {
			return nil, nil, nil, fmt.Errorf("native associated recapture missing arm")
		}
	}
	return recaptureNative(ctx, bindings, &associationBudget{})
}

func prepareProjectionCapture(ctx context.Context, locations map[releasepolicy.Arm]ArmLocation) (*projectionCapture, error) {
	if ctx == nil {
		return nil, fmt.Errorf("native projection capture requires context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(locations) != 2 {
		return nil, fmt.Errorf("native projection capture requires both arms")
	}
	bindings := make(map[releasepolicy.Arm]boundLocation, 2)
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		location, ok := locations[arm]
		if !ok {
			return nil, fmt.Errorf("native projection capture missing arm")
		}
		binding, err := bind(ctx, location)
		if err != nil {
			return nil, err
		}
		bindings[arm] = binding
	}
	canonical, sources, arms, err := recaptureWithAssociations(ctx, bindings)
	if err != nil {
		return nil, err
	}
	snapshot, err := evidence.JSONDigest(json.RawMessage(canonical))
	if err != nil {
		return nil, err
	}
	supplement, err := marshalCaptureAssociations(ctx, captureAssociations{Kind: associationKind, Version: associationVersion, Snapshot: *snapshot, Arms: arms})
	if err != nil {
		return nil, err
	}
	if len(supplement) > maxAssociationBytes {
		return nil, fmt.Errorf("native association encoding exceeds bounded limit")
	}
	seal, err := evidence.JSONDigest(json.RawMessage(supplement))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &projectionCapture{prepared: &Prepared{bindings, bytes.Clone(canonical), *snapshot, sources},
		associations: retainedAssociations{bytes.Clone(supplement), *seal}}, nil
}

// Encode in bounded units; unlike a whole-object Marshal, the aggregate buffer
// cannot allocate past the supplementary limit before the limit is checked.
func marshalCaptureAssociations(ctx context.Context, associated captureAssociations) ([]byte, error) {
	if ctx == nil {
		return nil, fmt.Errorf("native association encoding requires context")
	}
	var output []byte
	var sticky error
	write := func(raw []byte) {
		if sticky != nil {
			return
		}
		if err := ctx.Err(); err != nil {
			sticky = err
			return
		}
		if len(raw) > maxAssociationBytes-len(output) {
			sticky = fmt.Errorf("native association encoding exceeds bounded limit")
			return
		}
		if len(raw) > cap(output)-len(output) {
			growth := min(max(1024, cap(output)), maxAssociationBytes-cap(output))
			capacity := max(len(output)+len(raw), cap(output)+growth)
			grown := make([]byte, len(output), capacity)
			copy(grown, output)
			output = grown
		}
		output = append(output, raw...)
	}
	literal := func(value string) { write([]byte(value)) }
	value := func(value any) {
		if sticky != nil {
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			sticky = err
			return
		}
		write(raw)
	}
	literal(`{"kind":`)
	value(associated.Kind)
	literal(`,"version":`)
	value(associated.Version)
	literal(`,"snapshot":`)
	value(associated.Snapshot)
	literal(`,"arms":{`)
	for armIndex, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if armIndex != 0 {
			literal(",")
		}
		value(arm)
		literal(`:{"nodes":[`)
		row := associated.Arms[arm]
		for i, node := range row.Nodes {
			if i != 0 {
				literal(",")
			}
			value(node)
		}
		literal(`],"directories":[`)
		for i, directory := range row.Directories {
			if i != 0 {
				literal(",")
			}
			literal(`{"path":`)
			value(directory.Path)
			literal(`,"entries":[`)
			for j, entry := range directory.Entries {
				if j != 0 {
					literal(",")
				}
				value(entry)
			}
			literal(`]}`)
		}
		literal(`],"events":[`)
		for i, event := range row.Events {
			if i != 0 {
				literal(",")
			}
			value(event)
		}
		literal(`]}`)
	}
	literal(`}}`)
	if sticky != nil {
		return nil, sticky
	}
	return output, nil
}
