package nativesnapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/releasepolicy"
)

// Preflight reads bounded tokens against the exact supplementary schema before
// the typed decoder allocates any observation arrays.
type associationWireShape struct {
	kind     byte
	fields   map[string]*associationWireShape
	optional map[string]bool
	item     *associationWireShape
	count    *int
	limit    int
}

func associationSchema(budget *associationBudget, children *int) *associationWireShape {
	text := &associationWireShape{kind: 's'}
	number := &associationWireShape{kind: 'u'}
	object := func(fields map[string]*associationWireShape) *associationWireShape {
		return &associationWireShape{kind: 'o', fields: fields}
	}
	array := func(item *associationWireShape, counter *int, limit int) *associationWireShape {
		return &associationWireShape{kind: 'a', item: item, count: counter, limit: limit}
	}
	node := object(map[string]*associationWireShape{"path": text, "state": text, "kind": text})
	node.optional = map[string]bool{"kind": true}
	directory := object(map[string]*associationWireShape{"path": text,
		"entries": array(object(map[string]*associationWireShape{"name": text}), children, 2*maxEntries)})
	event := object(map[string]*associationWireShape{"ordinal": number, "phase": text, "scope_index": number,
		"role": text, "op": text, "path": text, "node": number, "source": number, "directory": number})
	event.optional = map[string]bool{"node": true, "source": true, "directory": true}
	arm := object(map[string]*associationWireShape{
		"nodes":       array(node, &budget.nodes, maxAssociationNodes),
		"directories": array(directory, &budget.directories, maxAssociationDirectories),
		"events":      array(event, &budget.events, maxAssociationEvents),
	})
	return object(map[string]*associationWireShape{"kind": text, "version": text,
		"snapshot": object(map[string]*associationWireShape{"sha256": text, "encoding": text}),
		"arms":     object(map[string]*associationWireShape{"baseline": arm, "candidate": arm})})
}

func scanAssociationValue(ctx context.Context, decoder *json.Decoder, shape *associationWireShape, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 16 {
		return fmt.Errorf("native association nesting exceeds bounded limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch shape.kind {
	case 's':
		text, ok := token.(string)
		if !ok || len(text) > maxAssociationPath {
			return fmt.Errorf("native association requires bounded string")
		}
	case 'u':
		number, ok := token.(json.Number)
		if !ok {
			return fmt.Errorf("native association requires unsigned integer")
		}
		if _, err := strconv.ParseUint(string(number), 10, 32); err != nil {
			return fmt.Errorf("invalid native association integer: %w", err)
		}
	case 'o':
		if token != json.Delim('{') {
			return fmt.Errorf("native association requires object")
		}
		seen := make(map[string]bool, len(shape.fields))
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			child, exists := shape.fields[key]
			if !ok || !exists || seen[key] {
				return fmt.Errorf("unknown or duplicate native association member %q", key)
			}
			seen[key] = true
			if err := scanAssociationValue(ctx, decoder, child, depth+1); err != nil {
				return err
			}
		}
		for key := range shape.fields {
			if !seen[key] && !shape.optional[key] {
				return fmt.Errorf("missing native association member %q", key)
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return fmt.Errorf("incomplete native association object")
		}
	case 'a':
		if token != json.Delim('[') {
			return fmt.Errorf("native association requires nonnull array")
		}
		for decoder.More() {
			if *shape.count >= shape.limit {
				return fmt.Errorf("native association decoded count exceeds bounded limit")
			}
			*shape.count++
			if err := scanAssociationValue(ctx, decoder, shape.item, depth+1); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return fmt.Errorf("incomplete native association array")
		}
	default:
		return fmt.Errorf("invalid native association schema")
	}
	return nil
}

func decodeAssociations(ctx context.Context, raw []byte) (captureAssociations, error) {
	if ctx == nil {
		return captureAssociations{}, fmt.Errorf("native association decoding requires context")
	}
	if len(raw) == 0 || len(raw) > maxAssociationBytes {
		return captureAssociations{}, fmt.Errorf("missing or oversized native associations")
	}
	if err := boundAssociationStrings(ctx, raw); err != nil {
		return captureAssociations{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	children := 0
	if err := scanAssociationValue(ctx, decoder, associationSchema(&associationBudget{}, &children), 0); err != nil {
		return captureAssociations{}, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return captureAssociations{}, fmt.Errorf("trailing native association JSON")
	}
	var result captureAssociations
	if err := json.Unmarshal(raw, &result); err != nil {
		return captureAssociations{}, err
	}
	return result, ctx.Err()
}

// Bound decoded string bytes before json.Decoder allocates a string token.
// Syntax and object membership are still checked by the schema token reader.
func boundAssociationStrings(ctx context.Context, raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if raw[i] != '"' {
			continue
		}
		size := 0
		i++
		for ; i < len(raw) && raw[i] != '"'; i++ {
			if size >= maxAssociationPath {
				return fmt.Errorf("native association string exceeds bounded limit")
			}
			if raw[i] != '\\' {
				_, width := utf8.DecodeRune(raw[i:])
				if width == 1 && raw[i] >= utf8.RuneSelf {
					size += 3
				} else {
					size += width
				}
				i += width - 1
				if size > maxAssociationPath {
					return fmt.Errorf("native association string exceeds bounded limit")
				}
				continue
			}
			i++
			if i >= len(raw) {
				return fmt.Errorf("incomplete native association string")
			}
			if raw[i] != 'u' {
				size++
				continue
			}
			if i+4 >= len(raw) {
				return fmt.Errorf("incomplete native association Unicode escape")
			}
			code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return fmt.Errorf("invalid native association Unicode escape: %w", err)
			}
			runeValue := rune(code)
			i += 4
			if runeValue >= 0xD800 && runeValue <= 0xDBFF && i+6 < len(raw) && raw[i+1] == '\\' && raw[i+2] == 'u' {
				low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err == nil && low >= 0xDC00 && low <= 0xDFFF {
					runeValue = utf16.DecodeRune(runeValue, rune(low))
					i += 6
				}
			}
			if utf16.IsSurrogate(runeValue) {
				runeValue = utf8.RuneError
			}
			size += utf8.RuneLen(runeValue)
			if size > maxAssociationPath {
				return fmt.Errorf("native association string exceeds bounded limit")
			}
		}
		if i >= len(raw) {
			return fmt.Errorf("incomplete native association string")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func detachProjectionInput(ctx context.Context, capture *projectionCapture) (detachedProjectionInput, error) {
	if ctx == nil {
		return detachedProjectionInput{}, fmt.Errorf("native projection detach requires context")
	}
	if err := ctx.Err(); err != nil {
		return detachedProjectionInput{}, err
	}
	if capture == nil || capture.prepared == nil {
		return detachedProjectionInput{}, fmt.Errorf("missing associated native capture; capture with associations first")
	}
	return validateProjectionInput(ctx, detachedProjectionInput{
		canonical: capture.prepared.canonical, snapshot: capture.prepared.seal, sources: capture.prepared.sources,
		associations: capture.associations})
}

func validateProjectionInput(ctx context.Context, input detachedProjectionInput) (detachedProjectionInput, error) {
	fail := func(err error) (detachedProjectionInput, error) { return detachedProjectionInput{}, err }
	if ctx == nil {
		return fail(fmt.Errorf("native projection validation requires context"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if len(input.canonical) == 0 || len(input.canonical) > maxProjectionBytes {
		return fail(fmt.Errorf("invalid native snapshot size"))
	}
	snapshot, err := evidence.JSONDigest(json.RawMessage(input.canonical))
	if err != nil || snapshot == nil || *snapshot != input.snapshot {
		return fail(fmt.Errorf("native snapshot seal changed"))
	}
	associated, err := decodeAssociations(ctx, input.associations.canonical)
	if err != nil {
		return fail(err)
	}
	seal, err := evidence.JSONDigest(json.RawMessage(input.associations.canonical))
	if err != nil || seal == nil || *seal != input.associations.seal {
		return fail(fmt.Errorf("native association seal changed"))
	}
	if associated.Kind != associationKind || associated.Version != associationVersion || associated.Snapshot != input.snapshot {
		return fail(fmt.Errorf("unsupported or mismatched native association supplement"))
	}
	totalBytes := 0
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if len(input.sources[arm]) > maxAssociationEvents {
			return fail(fmt.Errorf("native retained source count exceeds bounded observations"))
		}
		for _, source := range input.sources[arm] {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			limit := maxSourceBytes
			if slices.Contains(source.Roles, "executable") {
				limit = maxExecutableBytes
			}
			if len(source.Bytes) > limit || len(source.Bytes) > maxTotalBytes-totalBytes {
				return fail(fmt.Errorf("native retained source byte limit exceeded"))
			}
			totalBytes += len(source.Bytes)
		}
	}
	sources, err := detachedSources(ctx, input.canonical, input.sources)
	if err != nil {
		return fail(err)
	}
	var snapshots map[releasepolicy.Arm]armSnapshot
	if err := json.Unmarshal(input.canonical, &snapshots); err != nil {
		return fail(err)
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if err := validateSnapshotLabels(snapshots[arm]); err != nil {
			return fail(err)
		}
		if err := validateArmAssociations(ctx, associated.Arms[arm], snapshots[arm], sources[arm]); err != nil {
			return fail(err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return detachedProjectionInput{canonical: bytes.Clone(input.canonical), snapshot: input.snapshot, sources: sources,
		associations: retainedAssociations{bytes.Clone(input.associations.canonical), input.associations.seal}}, nil
}

func validateSnapshotLabels(snapshot armSnapshot) error {
	for _, label := range []string{snapshot.RootPath, snapshot.CWD} {
		if !filepath.IsAbs(label) || filepath.Clean(label) != label {
			return fmt.Errorf("native retained root/cwd requires canonical absolute historical labels")
		}
	}
	cwd, err := filepath.Rel(snapshot.RootPath, snapshot.CWD)
	if err != nil || canonical(cwd, true) != nil {
		return fmt.Errorf("native retained cwd is outside historical root")
	}
	if err := canonical(snapshot.EvalPath, false); err != nil {
		return err
	}
	if err := canonical(snapshot.Executable, false); err != nil {
		return err
	}
	if snapshot.ContextDir != "" {
		if filepath.IsAbs(snapshot.ContextDir) {
			relative, err := filepath.Rel(snapshot.RootPath, snapshot.ContextDir)
			if filepath.Clean(snapshot.ContextDir) != snapshot.ContextDir || err != nil || canonical(relative, true) != nil {
				return fmt.Errorf("native retained context is outside canonical historical root")
			}
		} else if err := canonical(snapshot.ContextDir, true); err != nil {
			return err
		}
	}
	return nil
}

func validateArmAssociations(ctx context.Context, associated armAssociations, snapshot armSnapshot, sources []source) error {
	if associated.Nodes == nil || associated.Directories == nil || associated.Events == nil || len(associated.Events) == 0 {
		return fmt.Errorf("incomplete native arm associations")
	}
	nodes := make(map[string]capturedNode, len(associated.Nodes))
	nodeIndexes := make(map[string]int, len(associated.Nodes))
	last := ""
	for index, node := range associated.Nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := associationPath(node.Path); err != nil {
			return err
		}
		if node.Path <= last {
			return fmt.Errorf("native association nodes are not unique and sorted")
		}
		if (node.State == presentNode && (node.Kind == nil || (*node.Kind != regularNode && *node.Kind != directoryNode))) ||
			(node.State == missingNode && node.Kind != nil) || (node.State != presentNode && node.State != missingNode) {
			return fmt.Errorf("invalid native association node shape")
		}
		nodes[node.Path], last = node, node.Path
		nodeIndexes[node.Path] = index
	}
	last = ""
	for _, directory := range associated.Directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := associationPath(directory.Path); err != nil {
			return err
		}
		node, ok := nodes[directory.Path]
		if directory.Path <= last || directory.Entries == nil || !ok || node.Kind == nil || *node.Kind != directoryNode {
			return fmt.Errorf("invalid native directory association")
		}
		prior := ""
		for _, entry := range directory.Entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := associationPath(entry.Name); err != nil {
				return err
			}
			if path.Base(entry.Name) != entry.Name || entry.Name == "." || entry.Name <= prior {
				return fmt.Errorf("invalid native association directory order/name")
			}
			prior = entry.Name
		}
		last = directory.Path
	}
	nodeUsed, dirUsed, sourceUsed := make([]bool, len(associated.Nodes)), make([]bool, len(associated.Directories)), make([]bool, len(sources))
	roles := make([]map[string]bool, len(sources))
	walk := ""
	for i, event := range associated.Events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := associationPath(event.Path); err != nil {
			return err
		}
		if event.Ordinal != uint32(i+1) {
			return fmt.Errorf("invalid native association ordinal")
		}
		switch event.Phase {
		case evalPhase, lockPhase, executablePhase:
			if event.ScopeIndex != 0 {
				return fmt.Errorf("invalid native association scope")
			}
		case discoveryPhase:
			if uint64(event.ScopeIndex) >= uint64(len(snapshot.Discovery)) {
				return fmt.Errorf("invalid discovery association scope")
			}
		case taskPhase, promptPhase, requestPhase:
			if uint64(event.ScopeIndex) >= uint64(len(snapshot.Tasks)) || (event.Phase == requestPhase && !snapshot.Tasks[event.ScopeIndex].Enabled) {
				return fmt.Errorf("invalid task association scope")
			}
		default:
			return fmt.Errorf("invalid native association phase")
		}
		switch event.Op {
		case readOperation:
			if event.Source == nil || event.Node != nil || event.Directory != nil || uint64(*event.Source) >= uint64(len(sources)) {
				return fmt.Errorf("invalid native read association")
			}
			index := *event.Source
			if sources[index].Path != event.Path || !slices.Contains(sources[index].Roles, event.Role) {
				return fmt.Errorf("native read source/role changed")
			}
			sourceUsed[index] = true
			if roles[index] == nil {
				roles[index] = map[string]bool{}
			}
			roles[index][event.Role] = true
		case kindOperation, resolveOperation, walkEntryOperation:
			if event.Node == nil || event.Source != nil || event.Directory != nil || uint64(*event.Node) >= uint64(len(associated.Nodes)) {
				return fmt.Errorf("invalid native node association")
			}
			node := associated.Nodes[*event.Node]
			if node.Path != event.Path {
				return fmt.Errorf("native node association path changed")
			}
			if node.State == missingNode && (event.Op != kindOperation || (event.Phase != lockPhase && event.Phase != discoveryPhase)) {
				return fmt.Errorf("unsupported native missing observation")
			}
			if event.Op == walkEntryOperation && walk == "" {
				return fmt.Errorf("native walk entry outside walk")
			}
			nodeUsed[*event.Node] = true
		case directoryOperation:
			if event.Directory == nil || event.Node != nil || event.Source != nil || uint64(*event.Directory) >= uint64(len(associated.Directories)) ||
				associated.Directories[*event.Directory].Path != event.Path || event.Phase != discoveryPhase {
				return fmt.Errorf("invalid native directory event")
			}
			dirUsed[*event.Directory] = true
		case walkBeginOperation, walkEndOperation:
			if event.Node != nil || event.Directory != nil || event.Source != nil || event.Phase != requestPhase {
				return fmt.Errorf("invalid native walk boundary")
			}
			if event.Op == walkBeginOperation {
				if walk != "" {
					return fmt.Errorf("nested native association walk")
				}
				walk = event.Path
			} else {
				if walk != event.Path {
					return fmt.Errorf("torn native association walk")
				}
				walk = ""
			}
		default:
			return fmt.Errorf("invalid native association operation")
		}
	}
	if walk != "" {
		return fmt.Errorf("incomplete native association walk")
	}
	// Directory rows observed by walking are used by the pure walk verifier;
	// their present node is also required even for an empty directory.
	for i, directory := range associated.Directories {
		if err := ctx.Err(); err != nil {
			return err
		}
		j := nodeIndexes[directory.Path]
		if dirUsed[i] {
			nodeUsed[j] = true
		}
		if nodeUsed[j] {
			dirUsed[i] = true
		}
	}
	for _, used := range nodeUsed {
		if !used {
			return fmt.Errorf("unused native node association")
		}
	}
	for _, used := range dirUsed {
		if !used {
			return fmt.Errorf("unused native directory association")
		}
	}
	for i, used := range sourceUsed {
		if !used || len(roles[i]) != len(sources[i].Roles) {
			return fmt.Errorf("incomplete native source role associations")
		}
	}
	return nil
}
