package assurance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"gopkg.in/yaml.v3"
)

type qualificationSourceRead struct {
	ID, Role, TaskID string
	Selector         *qualificationSelector
	Root             *os.Root
	Path             string
}

type qualificationSource struct {
	ID          string                 `json:"id"`
	Role        string                 `json:"role"`
	TaskID      string                 `json:"task_id"`
	Selector    *qualificationSelector `json:"selector"`
	ByteLength  uint64                 `json:"byte_length"`
	SHA256      string                 `json:"sha256"`
	BytesBase64 string                 `json:"bytes_base64"`
}

type qualificationSourceSet struct {
	Kind    string                `json:"kind"`
	Version string                `json:"version"`
	Sources []qualificationSource `json:"sources"`
}

type qualificationSources struct {
	document qualificationDocument
	native   qualificationDocument
}

type qualificationNativeTask struct {
	TaskID      string          `json:"task_id"`
	Declaration json.RawMessage `json:"declaration"`
}

type qualificationNativeSelection struct {
	ResolvedSpec json.RawMessage           `json:"resolved_spec"`
	Tasks        []qualificationNativeTask `json:"tasks"`
}

func qualificationSourceBytes(source qualificationSource) ([]byte, error) {
	size, err := qualificationBase64Size(source.BytesBase64, qualificationSourceByteLimit(source.Role))
	if err != nil || size != source.ByteLength {
		return nil, errors.New("qualification: source byte limit")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(source.BytesBase64)
	if err != nil || byteSHA256(data) != source.SHA256 {
		return nil, errors.New("qualification: source byte identity")
	}
	return data, nil
}

func qualificationSourceByteLimit(role string) uint64 {
	switch role {
	case "review", "references":
		return MaxLabelBytes
	case "implementation_executable":
		return qualificationExecutableLimit
	default:
		return qualificationDocumentLimit
	}
}

func qualificationParseSources(data []byte) (qualificationSources, error) {
	return qualificationParseSourcesBounded(data, qualificationBlobBounds{
		document: qualificationEncodedLimit, count: qualificationSourceLimit, total: qualificationTotalLimit,
		role: qualificationSourceByteLimit,
	}, nil)
}

func qualificationParseSourcesBounded(data []byte, bounds qualificationBlobBounds, materializing func()) (qualificationSources, error) {
	if err := qualificationPreflightBlobJSON(data, true, bounds); err != nil {
		return qualificationSources{}, err
	}
	if materializing != nil {
		materializing()
	}
	set, err := qualificationDecode[qualificationSourceSet](data, qualificationEncodedLimit)
	if err != nil {
		return qualificationSources{}, err
	}
	if set.Kind != "waza.qualification-source-set" || set.Version != qualificationVersion ||
		len(set.Sources) == 0 || len(set.Sources) > qualificationSourceLimit {
		return qualificationSources{}, errors.New("qualification: source profile")
	}
	claims := make([]qualificationEncodedClaim, 0, len(set.Sources))
	roles := map[string]int{}
	tasks := map[string]bool{}
	scoped := map[string]bool{}
	last := ""
	for _, source := range set.Sources {
		if !qualificationIdentifier(source.ID) || source.ID <= last || !qualificationDigest(source.SHA256) {
			return qualificationSources{}, errors.New("qualification: source ID/order/digest")
		}
		last = source.ID
		roles[source.Role]++
		switch source.Role {
		case "eval", "references", "review", "implementation_executable":
			if roles[source.Role] != 1 || source.TaskID != "" || source.Selector != nil || source.ByteLength == 0 {
				return qualificationSources{}, errors.New("qualification: singleton source role")
			}
		case "task":
			if !qualificationIdentifier(source.TaskID) || tasks[source.TaskID] || source.Selector != nil || source.ByteLength == 0 {
				return qualificationSources{}, errors.New("qualification: task source association")
			}
			tasks[source.TaskID] = true
		case "authored_input", "rubric":
			if source.Selector == nil || !source.Selector.valid() || source.TaskID != source.Selector.TaskID {
				return qualificationSources{}, errors.New("qualification: scoped source association")
			}
			key := source.Role + ":" + qualificationSelectorKey(*source.Selector)
			if scoped[key] {
				return qualificationSources{}, errors.New("qualification: duplicate scoped source")
			}
			scoped[key] = true
		default:
			return qualificationSources{}, errors.New("qualification: unsupported source role")
		}
		claims = append(claims, qualificationEncodedClaim{source.BytesBase64, source.ByteLength, qualificationSourceByteLimit(source.Role)})
	}
	if err := qualificationPreflightEncoded(claims, qualificationTotalLimit); err != nil {
		return qualificationSources{}, err
	}
	for _, role := range []string{"eval", "references", "review", "implementation_executable", "task"} {
		if roles[role] == 0 {
			return qualificationSources{}, errors.New("qualification: required source role")
		}
	}
	for _, source := range set.Sources {
		if _, err := qualificationSourceBytes(source); err != nil {
			return qualificationSources{}, err
		}
	}
	document, err := qualificationSealBounded(set, qualificationEncodedLimit, nil)
	return qualificationSources{document: document}, err
}

func qualificationReadSource(ctx context.Context, read qualificationSourceRead, remaining uint64) ([]byte, error) {
	if read.Root == nil || !filepath.IsLocal(read.Path) {
		return nil, errors.New("qualification: borrowed rooted local source required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := openReferenceDocument(read.Root, read.Path)
	if err != nil {
		return nil, errors.New("qualification: source unreadable")
	}
	info, statErr := file.Stat()
	limit := min(qualificationSourceByteLimit(read.Role), remaining)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || uint64(info.Size()) > limit {
		return nil, errors.Join(errors.New("qualification: regular bounded source required"), statErr, file.Close())
	}
	reader := io.LimitReader(file, int64(limit)+1)
	var data bytes.Buffer
	buffer := make([]byte, 64*1024)
	var readErr error
	for {
		if readErr = ctx.Err(); readErr != nil {
			break
		}
		n, err := reader.Read(buffer)
		data.Write(buffer[:n])
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	closeErr := file.Close()
	if readErr == nil {
		readErr = ctx.Err()
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if uint64(data.Len()) > limit {
		return nil, errors.New("qualification: growing source exceeded limit")
	}
	return data.Bytes(), nil
}

func qualificationAcquireSources(ctx context.Context, reads []qualificationSourceRead) (qualificationSources, error) {
	if len(reads) == 0 || len(reads) > qualificationSourceLimit {
		return qualificationSources{}, errors.New("qualification: source count")
	}
	set := qualificationSourceSet{Kind: "waza.qualification-source-set", Version: qualificationVersion, Sources: []qualificationSource{}}
	byID := map[string]qualificationSourceRead{}
	total := uint64(0)
	for _, read := range reads {
		if !qualificationIdentifier(read.ID) || byID[read.ID].ID != "" {
			return qualificationSources{}, errors.New("qualification: duplicate or invalid source ID")
		}
		byID[read.ID] = read
		data, err := qualificationReadSource(ctx, read, qualificationTotalLimit-total)
		if err != nil {
			return qualificationSources{}, err
		}
		total += uint64(len(data))
		var selector *qualificationSelector
		if read.Selector != nil {
			copy := *read.Selector
			selector = &copy
		}
		set.Sources = append(set.Sources, qualificationSource{
			ID: read.ID, Role: read.Role, TaskID: read.TaskID, Selector: selector,
			ByteLength: uint64(len(data)), SHA256: byteSHA256(data), BytesBase64: base64.StdEncoding.EncodeToString(data),
		})
	}
	slices.SortFunc(set.Sources, func(a, b qualificationSource) int { return strings.Compare(a.ID, b.ID) })
	data, err := qualificationMarshalBounded(set, qualificationEncodedLimit, nil)
	if err != nil {
		return qualificationSources{}, err
	}
	sources, err := qualificationParseSources(data)
	if err != nil {
		return qualificationSources{}, err
	}
	sources.native, err = qualificationLoadSelection(ctx, set, byID)
	if err == nil {
		err = qualificationValidateScopedSources(ctx, set, byID)
	}
	if err != nil {
		return qualificationSources{}, err
	}
	return sources, nil
}

func qualificationValidateScopedSources(ctx context.Context, set qualificationSourceSet, reads map[string]qualificationSourceRead) (returnErr error) {
	var labels *ReferenceSet
	var labelRead qualificationSourceRead
	for _, source := range set.Sources {
		if source.Role == "references" {
			data, err := qualificationSourceBytes(source)
			if err != nil {
				return err
			}
			labels, err = ParseReferences(data)
			if err != nil {
				return err
			}
			labelRead = reads[source.ID]
		}
	}
	document, err := labels.Document()
	if err != nil {
		return err
	}
	scoped := map[string]qualificationSource{}
	arm := ""
	for _, source := range set.Sources {
		if source.Selector == nil {
			continue
		}
		if arm != "" && source.Selector.ArmID != arm {
			return errors.New("qualification: scoped sources mix arms")
		}
		arm = source.Selector.ArmID
		key := source.Role + ":" + qualificationSelectorKey(*source.Selector)
		if _, present := scoped[key]; present {
			return errors.New("qualification: duplicate scoped source")
		}
		scoped[key] = source
	}
	if arm == "" {
		return errors.New("qualification: complete authored source inventory required")
	}
	store, err := newCalibrationRubricStore()
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, store.close()) }()
	root, err := os.OpenRoot(store.directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	for _, candidate := range document.Cases {
		if candidate.AuthoredInput == nil {
			return errors.New("qualification: non-authored input integration unsupported")
		}
		for _, check := range candidate.Checks {
			selector := qualificationSelector{arm, candidate.TaskID, check.RequirementID,
				qualificationCheck{check.Check.Scope, check.Check.AfterTurn, check.Check.Grader}, candidate.ID}
			key := "authored_input:" + qualificationSelectorKey(selector)
			source, present := scoped[key]
			read := reads[source.ID]
			if !present || read.Root != labelRead.Root ||
				filepath.Clean(read.Path) != filepath.Clean(filepath.Join(filepath.Dir(labelRead.Path), candidate.AuthoredInput.Path)) {
				return errors.New("qualification: actual authored source not selected by labels")
			}
			delete(scoped, key)
			data, err := qualificationSourceBytes(source)
			if err != nil {
				return err
			}
			name, err := store.write(data)
			if err != nil {
				return err
			}
			copy := candidate
			binding := *candidate.AuthoredInput
			binding.Path = filepath.Base(name)
			copy.AuthoredInput = &binding
			input, observation := verifyAuthoredInput(ctx, VerifyRequest{References: labels, SnapshotRoot: root}, copy, check, ChallengeObservation{})
			if input == nil {
				return fmt.Errorf("qualification: finite authored source not admitted: %s", observation.Reason)
			}
			if check.RubricContentSHA256 != nil {
				rubricKey := "rubric:" + qualificationSelectorKey(selector)
				rubric, present := scoped[rubricKey]
				if !present || rubric.SHA256 != *check.RubricContentSHA256 {
					return errors.New("qualification: original rubric inventory missing/mismatched")
				}
				delete(scoped, rubricKey)
			}
		}
	}
	if len(scoped) != 0 {
		return errors.New("qualification: extra scoped source inventory")
	}
	return nil
}

// Native offline loaders only see evaluator-private snapshot copies. Original
// rooted selection is proved separately; includes/globs are not guessed.
func qualificationLoadSelection(ctx context.Context, set qualificationSourceSet, reads map[string]qualificationSourceRead) (result qualificationDocument, returnErr error) {
	return qualificationLoadSelectionBounded(ctx, set, reads, qualificationDocumentLimit, nil)
}

func qualificationLoadSelectionBounded(ctx context.Context, set qualificationSourceSet, reads map[string]qualificationSourceRead, limit int, materializing func()) (result qualificationDocument, returnErr error) {
	return qualificationLoadSelectionWithHooks(ctx, set, reads, limit, materializing, nil)
}

func qualificationLoadSelectionWithHooks(ctx context.Context, set qualificationSourceSet, reads map[string]qualificationSourceRead, limit int, materializing func(), declarationMaterializing func(string)) (result qualificationDocument, returnErr error) {
	store, err := newCalibrationRubricStore()
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.close()) }()
	var eval qualificationSourceRead
	var spec *models.EvalSpec
	var references *ReferenceSet
	taskMap := map[string]*models.TestCase{}
	for _, source := range set.Sources {
		data, err := qualificationSourceBytes(source)
		if err != nil {
			return result, err
		}
		switch source.Role {
		case "references":
			references, err = ParseReferences(data)
			if err != nil {
				return result, err
			}
		case "review":
			if _, err := ParseReview(data); err != nil {
				return result, err
			}
		case "eval":
			if err := qualificationNoIncludes(data); err != nil {
				return result, err
			}
			name, err := store.write(data)
			if err != nil {
				return result, err
			}
			spec, err = models.LoadEvalSpecOffline(name)
			if err != nil {
				return result, errors.New("qualification: native snapshot eval invalid")
			}
			if spec.TasksFrom != "" || spec.Range != [2]int{} || len(spec.Tasks) == 0 {
				return result, errors.New("qualification: generated/ranged task selection unsupported")
			}
			if err := qualificationNativeYAMLShape(data, reflect.ValueOf(spec)); err != nil {
				return result, err
			}
			eval = reads[source.ID]
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	selected := map[string]bool{}
	for _, name := range spec.Tasks {
		if !filepath.IsLocal(name) || strings.ContainsAny(name, "*?[\\") {
			return result, errors.New("qualification: task globs/includes unsupported")
		}
		path := filepath.Clean(filepath.Join(filepath.Dir(eval.Path), name))
		if selected[path] {
			return result, errors.New("qualification: duplicate selected task path")
		}
		selected[path] = true
	}
	selection := qualificationNativeSelection{Tasks: []qualificationNativeTask{}}
	// Reserve all enclosing bytes, using a one-byte placeholder for the spec.
	// Each declaration is charged against this SAME budget before serialization.
	remaining := limit
	skeleton := qualificationNativeSelection{ResolvedSpec: json.RawMessage("0"), Tasks: []qualificationNativeTask{}}
	if err := qualificationProjectionSize(reflect.ValueOf(skeleton), &remaining, 0); err != nil {
		return result, err
	}
	remaining++
	beforeSpec := remaining
	if err := qualificationProjectionSize(reflect.ValueOf(spec), &remaining, 0); err != nil {
		return result, err
	}
	selection.ResolvedSpec, err = qualificationMarshalBounded(spec, beforeSpec-remaining, func() {
		if declarationMaterializing != nil {
			declarationMaterializing("spec")
		}
	})
	if err != nil {
		return result, err
	}
	for _, source := range set.Sources {
		if source.Role != "task" {
			continue
		}
		read := reads[source.ID]
		path := filepath.Clean(read.Path)
		if read.Root != eval.Root || !selected[path] {
			return result, errors.New("qualification: task source not selected by actual eval root/path")
		}
		delete(selected, path)
		data, err := qualificationSourceBytes(source)
		if err != nil {
			return result, err
		}
		if err := qualificationNoIncludes(data); err != nil {
			return result, err
		}
		name, err := store.write(data)
		if err != nil {
			return result, err
		}
		task, err := models.LoadTestCaseOffline(name)
		if err != nil || task.TestID != source.TaskID {
			return result, errors.New("qualification: task ID differs from actual selected source")
		}
		if err := qualificationNativeYAMLShape(data, reflect.ValueOf(task)); err != nil {
			return result, err
		}
		if len(selection.Tasks) > 0 {
			if err := qualificationSpendProjection(&remaining, 1); err != nil {
				return result, err
			}
		}
		row := qualificationNativeTask{TaskID: task.TestID, Declaration: json.RawMessage("0")}
		if err := qualificationProjectionSize(reflect.ValueOf(row), &remaining, 0); err != nil {
			return result, err
		}
		remaining++
		beforeTask := remaining
		if err := qualificationProjectionSize(reflect.ValueOf(task), &remaining, 0); err != nil {
			return result, err
		}
		declaration, err := qualificationMarshalBounded(task, beforeTask-remaining, func() {
			if declarationMaterializing != nil {
				declarationMaterializing(task.TestID)
			}
		})
		if err != nil {
			return result, err
		}
		taskMap[task.TestID] = task
		selection.Tasks = append(selection.Tasks, qualificationNativeTask{TaskID: task.TestID, Declaration: declaration})
	}
	if len(selected) != 0 {
		return result, errors.New("qualification: selected task source missing")
	}
	referenceDocument, err := references.Document()
	if err != nil {
		return result, err
	}
	for _, candidate := range referenceDocument.Cases {
		for _, check := range candidate.Checks {
			declaration, err := preflight.ResolveGrader(check.Check, taskMap[candidate.TaskID], spec)
			if err != nil {
				return result, errors.New("qualification: scoped grader absent from actual native selection")
			}
			var config any = declaration.Config
			var parameters models.GraderParameters
			var kind models.GraderKind
			if declaration.Config != nil {
				parameters = declaration.Config.Parameters
				kind = declaration.Config.Kind
			} else {
				config = declaration.Inline
				parameters = declaration.Inline.Parameters
				kind = declaration.Inline.Kind
			}
			binding, err := qualificationSeal(config)
			if err != nil || binding.sha256() != check.GraderDeclarationSHA256 {
				return result, errors.New("qualification: original native scoped grader binding mismatch")
			}
			prompt, isPrompt := parameters.(models.PromptGraderParameters)
			if kind == models.GraderKindPrompt || isPrompt {
				if !isPrompt || kind != models.GraderKindPrompt || prompt.ContinueSession ||
					(prompt.Mode != "" && prompt.Mode != models.PromptGraderModeIndependent) ||
					!calibrationLocalRubric(prompt.Rubric) || check.RubricContentSHA256 == nil {
					return result, errors.New("qualification: selected prompt requires supported independent local rubric and original binding")
				}
			}
			foundRubric := false
			for _, source := range set.Sources {
				if source.Role != "rubric" || source.Selector == nil ||
					source.Selector.TaskID != candidate.TaskID || source.Selector.CaseID != candidate.ID ||
					source.Selector.RequirementID != check.RequirementID ||
					source.Selector.Check != (qualificationCheck{check.Check.Scope, check.Check.AfterTurn, check.Check.Grader}) {
					continue
				}
				if !isPrompt || filepath.Clean(reads[source.ID].Path) != filepath.Clean(prompt.Rubric) ||
					check.RubricContentSHA256 == nil || source.SHA256 != *check.RubricContentSHA256 {
					return result, errors.New("qualification: rubric source not selected by actual native parameters")
				}
				foundRubric = true
			}
			if isPrompt && !foundRubric {
				return result, errors.New("qualification: selected native prompt original rubric source missing")
			}
		}
	}
	slices.SortFunc(selection.Tasks, func(a, b qualificationNativeTask) int { return strings.Compare(a.TaskID, b.TaskID) })
	if err := ctx.Err(); err != nil {
		return result, err
	}
	return qualificationSealBounded(selection, limit, materializing)
}

func qualificationNoIncludes(data []byte) error {
	if !utf8ValidQualificationSource(data) {
		return errors.New("qualification: source Unicode invalid")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return errors.New("qualification: snapshot YAML invalid")
	}
	var trailing yaml.Node
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("qualification: multiple YAML documents unsupported")
	}
	var visit func(*yaml.Node, int) error
	visit = func(node *yaml.Node, depth int) error {
		if depth > 64 || node.Kind == yaml.AliasNode {
			return errors.New("qualification: deep/aliased YAML unsupported")
		}
		if node.Kind == yaml.MappingNode {
			seen := map[string]bool{}
			for i := 0; i < len(node.Content); i += 2 {
				key := node.Content[i].Value
				if seen[key] {
					return errors.New("qualification: duplicate YAML key")
				}
				seen[key] = true
				switch key {
				case "ref", "$ref", "prompt_file", "tasks_from", "include", "includes", "schema_file":
					return errors.New("qualification: external include/ref resolution unsupported")
				}
			}
		}
		for _, child := range node.Content {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(&node, 0)
}

func utf8ValidQualificationSource(data []byte) bool {
	// JSON escaped surrogates are validated by the JSON readers; YAML rejects
	// invalid raw Unicode during node decoding, including non-UTF8 bytes.
	return !bytes.Contains(data, []byte{0}) && utf8.Valid(data)
}

func (sources qualificationSources) source(id string) ([]byte, error) {
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	if err != nil {
		return nil, err
	}
	for _, source := range set.Sources {
		if source.ID == id {
			return qualificationSourceBytes(source)
		}
	}
	return nil, fmt.Errorf("qualification: source ID unavailable")
}

// Native custom YAML unmarshallers can warn rather than reject unknown keys.
// Check their actual concrete decoded types, including grader parameters.
func qualificationNativeYAMLShape(data []byte, value reflect.Value) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	var check func(*yaml.Node, reflect.Value) error
	check = func(node *yaml.Node, value reflect.Value) error {
		if node.Kind == yaml.DocumentNode {
			return check(node.Content[0], value)
		}
		for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
			value = value.Elem()
		}
		if !value.IsValid() {
			return errors.New("qualification: unsupported null native declaration")
		}
		switch value.Kind() {
		case reflect.Struct:
			if node.Kind != yaml.MappingNode {
				return errors.New("qualification: native declaration object required")
			}
			fields := map[string]reflect.Value{}
			var collect func(reflect.Value)
			collect = func(value reflect.Value) {
				for i := range value.NumField() {
					tag := value.Type().Field(i).Tag.Get("yaml")
					name, options, _ := strings.Cut(tag, ",")
					if name == "-" {
						continue
					}
					if strings.Contains(options, "inline") {
						collect(value.Field(i))
					} else {
						if name == "" {
							name = strings.ToLower(value.Type().Field(i).Name)
						}
						fields[name] = value.Field(i)
					}
				}
			}
			collect(value)
			for i := 0; i < len(node.Content); i += 2 {
				field, ok := fields[node.Content[i].Value]
				if !ok {
					return errors.New("qualification: unknown native YAML field")
				}
				if err := check(node.Content[i+1], field); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			if node.Kind != yaml.SequenceNode || len(node.Content) != value.Len() {
				return errors.New("qualification: native declaration array required")
			}
			for i, child := range node.Content {
				if err := check(child, value.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check(&document, value)
}
