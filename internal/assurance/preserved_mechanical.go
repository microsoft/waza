package assurance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/graders/argmatcher"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/internal/snapshot"
)

// Adapter admission caps are independent of capture policy and workspace
// materialization, neither of which bounds caller-supplied preserved files.
const (
	preservedMaxFileBytes     = 4 * 1024 * 1024
	preservedMaxSelectedBytes = 8 * 1024 * 1024
	preservedMaxSelectedFiles = 256
)

// PreservedMechanicalInput selects in-memory native artifacts, not an execution
// source or a serialized assurance profile. Complete synthetic fixtures certify
// only their authored finite tape, never historical collector completeness.
type PreservedMechanicalInput struct {
	Task           *models.TestCase
	Spec           *models.EvalSpec
	Check          models.RequirementCheck
	SnapshotBytes  []byte
	ManifestSHA256 string
	Evidence       []models.EvidenceReference
}

// ObservePreservedMechanical does not reopen sources, run an engine, authenticate
// review, or establish label agreement. Actual captures with unknown/partial
// completeness remain insufficient even when their runtime says live or mock.
func ObservePreservedMechanical(ctx context.Context, input PreservedMechanicalInput) Observation {
	return observePreservedMechanical(ctx, input, preservedMechanicalHooks{})
}

// Per-call hooks exercise cancellation and cleanup failures without global state.
type preservedMechanicalHooks struct {
	prepared func(*graders.Context)
	close    func(func() error) error
}

func observePreservedMechanical(ctx context.Context, input PreservedMechanicalInput, hooks preservedMechanicalHooks) (observation Observation) {
	observation.Check = input.Check
	if input.Task != nil {
		observation.TaskID = input.Task.TestID
	}
	fail := func(state ObservationState, err error) Observation {
		observation.State, observation.Err = state, err
		return observation
	}
	failDeclaration := func(operation string, err error) Observation {
		state := preservedDeclarationErrorState(err)
		if state == NotAssessed {
			err = errors.Join(schemaloader.ErrExternalReference, err)
		}
		return fail(state, fmt.Errorf("%s: %w", operation, err))
	}
	if err := ctx.Err(); err != nil {
		return fail(OperationalError, fmt.Errorf("observing preserved evidence: %w", err))
	}
	if len(input.SnapshotBytes) == 0 || len(input.SnapshotBytes) > maxSnapshotBytes {
		return fail(Invalid, fmt.Errorf("preserved raw snapshot must contain 1 to %d bytes", maxSnapshotBytes))
	}
	if input.Task == nil || input.Spec == nil {
		return fail(Invalid, errors.New("preserved grading requires a native task and eval specification"))
	}
	if err := validateIdentity(ReferenceInput{TaskID: input.Task.TestID, Check: input.Check}); err != nil {
		return fail(Invalid, err)
	}
	if input.Check.Scope == "checkpoint" {
		return fail(NotAssessed, errors.New("preserved tape cannot establish checkpoint identity"))
	}
	declaration, err := preflight.ResolveGrader(input.Check, input.Task, input.Spec)
	if err != nil {
		return fail(Invalid, fmt.Errorf("resolving preserved grader: %w", err))
	}
	if declaration.Config != nil && declaration.Config.Ref != "" {
		return fail(NotAssessed, errors.New("referenced native graders require separately resolved declarations"))
	}
	// Copy only the selected declaration; unrelated declarations cannot load
	// schemas, run programs, or affect admission of this observation.
	task := &models.TestCase{TestID: input.Task.TestID}
	spec := &models.EvalSpec{}
	var parameters models.GraderParameters
	if declaration.Config != nil {
		parameters = declaration.Config.Parameters
	} else if declaration.Inline != nil {
		parameters = declaration.Inline.Parameters
	}
	if parameters == nil {
		return fail(Invalid, errors.New("selected native grader parameters are unresolved"))
	}
	// Native matcher JSON decoding compiles schemas with the legacy loader.
	// Prove they are self-contained using the offline loader before copying.
	if err := preflightPreservedMatchers(parameters); err != nil {
		return failDeclaration("validating preserved argument schemas", err)
	}
	if declaration.Config != nil {
		copied, err := calibrationConfigCopy(*declaration.Config)
		if err != nil {
			return failDeclaration("detaching preserved grader", err)
		}
		spec.Graders = []models.GraderConfig{copied}
		parameters = copied.Parameters
	} else if declaration.Inline != nil {
		copied, err := calibrationValidatorsCopy([]models.ValidatorInline{*declaration.Inline})
		if err != nil {
			return failDeclaration("detaching preserved grader", err)
		}
		task.Validators = copied
		parameters = copied[0].Parameters
	} else {
		return fail(Invalid, errors.New("preserved grader declaration is unavailable"))
	}
	var paths []string
	switch params := parameters.(type) {
	case models.ToolCallsGraderParameters, models.ToolConstraintGraderParameters, models.ActionSequenceGraderParameters:
	case models.FileGraderParameters:
		if len(params.MustNotExist) != 0 {
			return fail(InsufficientEvidence, errors.New("preserved file subset cannot prove must_not_exist"))
		}
		paths = slices.Clone(params.MustExist)
		for _, pattern := range params.ContentPatterns {
			if !slices.Contains(paths, pattern.Path) {
				paths = append(paths, pattern.Path)
			}
		}
	default:
		return fail(NotAssessed, errors.New("selected grader requires evidence or execution not supported by preserved mechanical grading"))
	}
	if err := graders.ValidateConfig(input.Check.Grader, parameters); err != nil {
		return failDeclaration("validating preserved grader", err)
	}
	data := bytes.Clone(input.SnapshotBytes)
	references := slices.Clone(input.Evidence)
	// The existing Unicode validator assumes syntactically valid JSON. Check
	// syntax without decoding, then reject lossy escapes before any raw parsing.
	if !json.Valid(data) {
		return fail(Invalid, errors.New("preserved snapshot is not valid JSON"))
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return fail(Invalid, fmt.Errorf("validating preserved raw Unicode escapes: %w", err))
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return fail(Invalid, fmt.Errorf("parsing preserved raw JSON: %w", err))
	}
	root, ok := value.(map[string]any)
	if !ok {
		return fail(Invalid, errors.New("preserved snapshot must be a raw JSON object"))
	}
	if version, ok := root["schemaVersion"].(string); !ok || version == "" || root["kind"] != snapshot.Kind {
		return fail(Invalid, errors.New("preserved snapshot requires explicit native kind and schemaVersion"))
	}
	if err := models.ValidateNativeJSONKeys(data, snapshot.Snapshot{}); err != nil {
		return fail(Invalid, err)
	}
	if root["evidence"] == nil {
		return fail(InsufficientEvidence, errors.New("preserved native manifest is unavailable"))
	}
	manifestBytes, err := json.Marshal(root["evidence"])
	if err != nil {
		return fail(Invalid, fmt.Errorf("reading preserved manifest: %w", err))
	}
	var manifest models.EvidenceManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fail(Invalid, fmt.Errorf("decoding preserved manifest: %w", err))
	}
	if err := evidence.ValidateNative(&manifest); err != nil {
		return fail(Invalid, err)
	}
	origin := manifest.Origin
	if input.ManifestSHA256 == "" || input.ManifestSHA256 != manifest.SHA256 ||
		!evidence.CompleteOrigin(origin) || origin.TaskID != task.TestID {
		return fail(Invalid, errors.New("preserved manifest selection or native origin is inconsistent"))
	}
	selected := make(map[string]bool)
	if _, file := parameters.(models.FileGraderParameters); !file && root["toolEvents"] == nil {
		return fail(InsufficientEvidence, errors.New("native toolEvents is absent or null, not an empty tape"))
	}
	if _, file := parameters.(models.FileGraderParameters); file && root["workspaceFiles"] == nil {
		return fail(InsufficientEvidence, errors.New("native workspaceFiles is absent or null"))
	}
	boundedFiles := make(map[string]bool)
	selectedBytes := 0
	for _, reference := range references {
		artifact, err := evidence.Resolve(&manifest, reference)
		if err != nil {
			return fail(Invalid, fmt.Errorf("resolving preserved reference: %w", err))
		}
		if artifact.Availability != "captured" || artifact.ContentDigest == nil ||
			artifact.Completeness != "complete" || artifact.Redacted {
			return fail(InsufficientEvidence, errors.New("selected native content is unavailable, incomplete, or redacted"))
		}
		if artifact.ID == "tool-events" || artifact.Kind == "tool_events" {
			if artifact.ID != "tool-events" || artifact.Kind != "tool_events" ||
				artifact.Document != "snapshot" || artifact.Pointer != "/toolEvents" ||
				artifact.ContentDigest.Encoding != "json-v1" {
				return fail(Invalid, errors.New("selected native tool-events identity or raw locator is inconsistent"))
			}
		}
		if strings.HasPrefix(artifact.ID, "workspace-file/") || artifact.Kind == "workspace_file" {
			state, size, err := preservedSelectedFileSize(root, artifact)
			if err != nil {
				return fail(state, err)
			}
			if !boundedFiles[artifact.ID] {
				boundedFiles[artifact.ID] = true
				selectedBytes += size
				if len(boundedFiles) > preservedMaxSelectedFiles || selectedBytes > preservedMaxSelectedBytes {
					return fail(Invalid, fmt.Errorf("selected preserved files exceed adapter caps of %d files or %d decoded bytes", preservedMaxSelectedFiles, preservedMaxSelectedBytes))
				}
			}
		}
		if reference.Pointer == "" {
			selected[artifact.ID] = true
		} else if state, err := verifyPreservedRawReference(root, &manifest, reference); err != nil {
			return fail(state, err)
		}
	}
	gCtx := &graders.Context{TestCase: task}
	if _, file := parameters.(models.FileGraderParameters); file {
		state, err := verifyPreservedFiles(root, &snapshot.Snapshot{Evidence: &manifest}, paths, selected)
		if err != nil {
			return fail(state, err)
		}
	} else {
		if !selected["tool-events"] {
			return fail(InsufficientEvidence, errors.New("complete whole tool-events reference is required"))
		}
		artifact, err := evidence.Resolve(&manifest, models.EvidenceReference{Origin: origin, ArtifactID: "tool-events"})
		if err != nil {
			return fail(Invalid, err)
		}
		if artifact.Kind != "tool_events" || artifact.Document != "snapshot" || artifact.Pointer != "/toolEvents" ||
			artifact.ContentDigest.Encoding != "json-v1" {
			return fail(Invalid, errors.New("native tool-events artifact has an inconsistent kind or locator"))
		}
	}
	for _, reference := range references {
		if reference.Pointer != "" {
			continue // Already verified before whole-artifact selection.
		}
		if state, err := verifyPreservedRawReference(root, &manifest, reference); err != nil {
			return fail(state, err)
		}
	}
	if _, file := parameters.(models.FileGraderParameters); !file {
		state, err := projectPreservedTape(root["toolEvents"], parameters, gCtx)
		if err != nil {
			return fail(state, err)
		}
	}
	// Parse native DTOs only after raw tape admission, so absent/null fields
	// cannot silently become zero values or an observed empty argument object.
	snap, err := snapshot.ParseSnapshot(data, "preserved mechanical input")
	if err != nil {
		return fail(Invalid, err)
	}
	if snap.EvalID != origin.EvalID || snap.Task.TestID != origin.TaskID || snap.Task.RunNumber != origin.RunNumber {
		return fail(Invalid, errors.New("preserved snapshot eval/task/run does not match native manifest origin"))
	}
	if _, file := parameters.(models.FileGraderParameters); file {
		if err := snapshot.VerifyWorkspace(snap, origin, input.ManifestSHA256, paths); err != nil {
			return fail(Invalid, fmt.Errorf("verifying preserved workspace: %w", err))
		}
		if err := ctx.Err(); err != nil {
			return fail(OperationalError, fmt.Errorf("materializing preserved workspace: %w", err))
		}
		workspace, err := snapshot.MaterializeWorkspace(snap, origin, input.ManifestSHA256, paths)
		if err != nil {
			return fail(OperationalError, fmt.Errorf("materializing preserved workspace: %w", err))
		}
		gCtx.WorkspaceDir = workspace.Path()
		defer func() {
			close := workspace.Close
			var err error
			if hooks.close != nil {
				err = hooks.close(close)
			} else {
				err = close()
			}
			if err != nil {
				observation.State = OperationalError
				observation.Err = errors.Join(observation.Err, fmt.Errorf("cleaning preserved workspace: %w", err))
			}
		}()
	}
	if hooks.prepared != nil {
		hooks.prepared(gCtx)
	}
	if err := ctx.Err(); err != nil {
		return fail(OperationalError, fmt.Errorf("grading preserved evidence: %w", err))
	}
	// ObserveDeclaredMechanical's general argument preflight checks every event,
	// not only consumed arguments. Keep unrelated canonical nonobjects unchanged
	// and use native Grade directly after this adapter's selected-check preflight.
	direct := gCtx.WorkspaceDir != ""
	for _, event := range gCtx.ToolEvents {
		if event.Args != nil {
			if _, object := event.Args.(map[string]any); !object {
				direct = true
			}
		}
	}
	if !direct {
		return ObserveDeclaredMechanical(ctx, task, spec, input.Check, gCtx)
	}
	grader, err := graders.Create(input.Check.Grader, parameters)
	if err != nil {
		return fail(Invalid, fmt.Errorf("creating preserved native grader: %w", err))
	}
	observation.Result, err = grader.Grade(ctx, gCtx)
	if err != nil {
		return fail(OperationalError, fmt.Errorf("grading preserved evidence: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return fail(OperationalError, fmt.Errorf("grading preserved evidence interrupted: %w", err))
	}
	if observation.Result == nil {
		return fail(OperationalError, errors.New("preserved native grader returned no result"))
	}
	observation.State = Observed
	return observation
}

func preservedSelectedFileSize(root map[string]any, artifact *models.EvidenceArtifact) (ObservationState, int, error) {
	if artifact.Kind != "workspace_file" || artifact.Document != "snapshot" ||
		artifact.ContentDigest.Encoding != "json-v1" || !strings.HasPrefix(artifact.Pointer, "/workspaceFiles/") {
		return Invalid, 0, errors.New("selected preserved file kind or raw locator is inconsistent")
	}
	if root["workspaceFiles"] == nil {
		return InsufficientEvidence, 0, errors.New("selected preserved workspace files are absent or null")
	}
	if files, ok := root["workspaceFiles"].([]any); ok && len(files) == 0 {
		return InsufficientEvidence, 0, errors.New("selected preserved workspace file content is missing")
	}
	raw, err := pointerValue(root, artifact.Pointer)
	if err != nil {
		return Invalid, 0, fmt.Errorf("resolving selected preserved file locator: %w", err)
	}
	file, ok := raw.(map[string]any)
	if !ok {
		return Invalid, 0, errors.New("selected preserved file must be a raw object")
	}
	for _, field := range []string{"path", "content", "sha256"} {
		if file[field] == nil {
			return InsufficientEvidence, 0, fmt.Errorf("selected preserved file requires explicit %s", field)
		}
		if _, ok := file[field].(string); !ok {
			return Invalid, 0, fmt.Errorf("selected preserved file %s must be a string", field)
		}
	}
	path, _ := file["path"].(string)
	content, _ := file["content"].(string)
	sha, _ := file["sha256"].(string)
	index, err := strconv.Atoi(strings.TrimPrefix(artifact.Pointer, "/workspaceFiles/"))
	if err != nil || index < 0 || artifact.Pointer != fmt.Sprintf("/workspaceFiles/%d", index) ||
		artifact.ID != "workspace-file/"+path {
		return Invalid, 0, errors.New("selected preserved file ID or exact raw ordinal does not match its content")
	}
	if err := validateArtifactPath(path); err != nil {
		return Invalid, 0, err
	}
	if len(content) > preservedMaxFileBytes {
		return Invalid, 0, fmt.Errorf("selected preserved file %q exceeds adapter cap of %d decoded bytes", path, preservedMaxFileBytes)
	}
	redacted, ok := file["redacted"].(bool)
	if !ok || redacted {
		return InsufficientEvidence, 0, errors.New("selected preserved file requires explicit unredacted content")
	}
	digest := sha256.Sum256([]byte(content))
	if hex.EncodeToString(digest[:]) != sha {
		return Invalid, 0, errors.New("selected preserved file content SHA256 is inconsistent")
	}
	return Observed, len(content), nil
}

func preflightPreservedMatchers(parameters models.GraderParameters) error {
	check := func(matchers map[string]argmatcher.Matcher) error {
		for _, matcher := range matchers {
			if err := matcher.CompileWithLoader(schemaloader.Offline{}); err != nil {
				return err
			}
		}
		return nil
	}
	switch params := parameters.(type) {
	case models.ToolCallsGraderParameters:
		for _, expectation := range params.Expect {
			if err := check(expectation.Args); err != nil {
				return err
			}
		}
	case models.ToolConstraintGraderParameters:
		specs := append(slices.Clone(params.ExpectTools), params.RejectTools...)
		if params.AllowOnly != nil {
			specs = append(specs, (*params.AllowOnly)...)
		}
		for _, spec := range specs {
			if err := check(spec.Args); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyPreservedRawReference(root map[string]any, manifest *models.EvidenceManifest, reference models.EvidenceReference) (ObservationState, error) {
	artifact, err := evidence.Resolve(manifest, reference)
	if err != nil {
		return Invalid, err
	}
	raw, err := pointerValue(root, artifact.Pointer)
	if err != nil {
		return Invalid, fmt.Errorf("resolving preserved raw locator: %w", err)
	}
	content, err := json.Marshal(raw)
	if err != nil {
		return OperationalError, fmt.Errorf("reading preserved raw artifact: %w", err)
	}
	if artifact.ContentDigest.Encoding == "utf8" {
		text, ok := raw.(string)
		if !ok {
			return Invalid, errors.New("preserved UTF-8 artifact must locate a string")
		}
		content = []byte(text)
	}
	if err := evidence.VerifyContent(manifest, reference, content, true, true); err != nil {
		return Invalid, fmt.Errorf("verifying preserved raw artifact: %w", err)
	}
	return Observed, nil
}

func preservedDeclarationErrorState(err error) ObservationState {
	if loadErr, ok := errors.AsType[*jsonschema.LoadURLError](err); ok && errors.Is(loadErr.Err, schemaloader.ErrExternalReference) {
		return NotAssessed
	}
	return Invalid
}

func projectPreservedTape(raw any, parameters models.GraderParameters, target *graders.Context) (ObservationState, error) {
	if raw == nil {
		return InsufficientEvidence, errors.New("native toolEvents is absent or null, not an empty tape")
	}
	tape, ok := raw.([]any)
	if !ok {
		return Invalid, errors.New("native toolEvents must be an array")
	}
	session := &models.SessionDigest{
		ToolCallCount: len(tape), ToolCalls: make([]models.ToolCall, 0, len(tape)), ToolsUsed: make([]string, 0, len(tape)),
	}
	events := make([]models.ToolEvent, 0, len(tape))
	ids := map[string]bool{}
	for index, rawEvent := range tape {
		event, ok := rawEvent.(map[string]any)
		if !ok {
			return Invalid, errors.New("native event must be an object")
		}
		for _, field := range []string{"tool_name", "sequence", "turn", "success"} {
			if event[field] == nil {
				return InsufficientEvidence, fmt.Errorf("native event %d requires explicit %s", index+1, field)
			}
		}
		name, ok := event["tool_name"].(string)
		if !ok || name == "" {
			return Invalid, errors.New("native event tool_name must be a nonempty string")
		}
		sequence, sequenceOK := preservedInteger(event["sequence"])
		turn, turnOK := preservedInteger(event["turn"])
		success, successOK := event["success"].(bool)
		if !sequenceOK || sequence != int64(index+1) || !turnOK || turn < 0 || !successOK {
			return Invalid, errors.New("native event sequence, turn, or success has an invalid value or type")
		}
		if id, present := event["tool_call_id"]; present {
			text, ok := id.(string)
			if !ok || text != "" && ids[text] {
				return Invalid, errors.New("native tool_call_id must be a string and nonempty IDs must be unique")
			}
			if text != "" {
				ids[text] = true
			}
		}
		if duration, present := event["duration_ms"]; present {
			number, ok := preservedInteger(duration)
			if !ok || number < 0 {
				return Invalid, errors.New("native duration_ms must be a nonnegative integer")
			}
		}
		consumes := preservedConsumesArguments(parameters, name)
		args, object := event["args"].(map[string]any)
		if consumes && event["args"] == nil {
			return InsufficientEvidence, fmt.Errorf("native tool %q requires explicit nonnull arguments", name)
		}
		if consumes && !object {
			return OperationalError, fmt.Errorf("native tool %q consumed arguments must be an object", name)
		}
		// Decode from the detached raw value, retaining exact JSON numbers and
		// canonical event values independently of the compatibility projection.
		data, err := json.Marshal(event)
		if err != nil {
			return Invalid, err
		}
		var canonical models.ToolEvent
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&canonical); err != nil {
			return Invalid, fmt.Errorf("decoding admitted native event: %w", err)
		}
		call := models.ToolCall{ID: canonical.ToolCallID, Name: name, Success: success}
		if object {
			call.Arguments.Extra = make(map[string]any)
			known := map[string]*string{
				"path": &call.Arguments.Path, "file_text": &call.Arguments.FileText,
				"command": &call.Arguments.Command, "description": &call.Arguments.Description, "skill": &call.Arguments.Skill,
			}
			for key, value := range args {
				if field, known := known[key]; known {
					text, stringValue := value.(string)
					if !stringValue {
						if preservedConsumesKnownField(parameters, name, key) {
							return OperationalError, fmt.Errorf("native tool %q argument %q must be a string without coercion", name, key)
						}
						continue
					}
					*field = text
				} else {
					call.Arguments.Extra[key] = value
				}
			}
		}
		events = append(events, canonical)
		session.ToolCalls = append(session.ToolCalls, call)
		session.ToolsUsed = append(session.ToolsUsed, name)
	}
	target.Session, target.ToolEvents = session, events
	return Observed, nil
}

func preservedInteger(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	integer, err := strconv.ParseInt(string(number), 10, 64)
	return integer, err == nil
}

func preservedConsumesArguments(parameters models.GraderParameters, name string) bool {
	match := func(pattern string) bool {
		ok, _ := regexp.MatchString("(?i)"+pattern, name)
		return ok
	}
	switch params := parameters.(type) {
	case models.ToolCallsGraderParameters:
		for _, expectation := range params.Expect {
			if len(expectation.Args) > 0 && match(expectation.Tool) {
				return true
			}
		}
	case models.ToolConstraintGraderParameters:
		consumes := func(spec models.ToolSpecParameters) bool {
			return len(spec.Args) > 0 || spec.CommandPattern != "" || spec.PathPattern != "" || spec.SkillPattern != ""
		}
		for _, specs := range [][]models.ToolSpecParameters{params.ExpectTools, params.RejectTools} {
			for _, spec := range specs {
				if consumes(spec) && match(strings.TrimSpace(spec.Tool)) {
					return true
				}
			}
		}
		if params.AllowOnly != nil {
			for _, spec := range *params.AllowOnly {
				if consumes(spec) && models.MatchesToolCallName(strings.TrimSpace(spec.Tool), name) {
					return true
				}
			}
		}
	}
	return false
}

func preservedConsumesKnownField(parameters models.GraderParameters, name, key string) bool {
	match := func(pattern string) bool {
		ok, _ := regexp.MatchString("(?i)"+pattern, name)
		return ok
	}
	switch params := parameters.(type) {
	case models.ToolCallsGraderParameters:
		for _, expectation := range params.Expect {
			if _, exists := expectation.Args[key]; exists && match(expectation.Tool) {
				return true
			}
		}
	case models.ToolConstraintGraderParameters:
		consumes := func(spec models.ToolSpecParameters) bool {
			_, exists := spec.Args[key]
			return exists || key == "path" && spec.PathPattern != "" ||
				key == "command" && spec.CommandPattern != "" || key == "skill" && spec.SkillPattern != ""
		}
		for _, specs := range [][]models.ToolSpecParameters{params.ExpectTools, params.RejectTools} {
			for _, spec := range specs {
				if consumes(spec) && match(strings.TrimSpace(spec.Tool)) {
					return true
				}
			}
		}
		if params.AllowOnly != nil {
			for _, spec := range *params.AllowOnly {
				if consumes(spec) && models.MatchesToolCallName(strings.TrimSpace(spec.Tool), name) {
					return true
				}
			}
		}
	}
	return false
}

func verifyPreservedFiles(root map[string]any, snap *snapshot.Snapshot, paths []string, selected map[string]bool) (ObservationState, error) {
	rawFiles, ok := root["workspaceFiles"].([]any)
	if !ok {
		return InsufficientEvidence, errors.New("preserved workspace files are absent or null")
	}
	for _, path := range paths {
		if err := validateArtifactPath(path); err != nil {
			return Invalid, err
		}
		if !selected["workspace-file/"+path] {
			return InsufficientEvidence, fmt.Errorf("whole workspace-file/%s reference is required", path)
		}
		found := false
		for index, raw := range rawFiles {
			file, ok := raw.(map[string]any)
			if !ok {
				return Invalid, errors.New("preserved workspace file must be an object")
			}
			if file["path"] == nil {
				return InsufficientEvidence, errors.New("preserved workspace file requires an explicit path")
			}
			name, ok := file["path"].(string)
			if !ok {
				return Invalid, errors.New("preserved workspace file path must be a string")
			}
			if name != path {
				continue
			}
			found = true
			for _, field := range []string{"path", "content", "sha256"} {
				if file[field] == nil {
					return InsufficientEvidence, fmt.Errorf("preserved file requires explicit %s", field)
				}
				if _, ok := file[field].(string); !ok {
					return Invalid, fmt.Errorf("preserved file %s must be a string", field)
				}
			}
			if _, ok := file["redacted"].(bool); !ok {
				return InsufficientEvidence, errors.New("preserved file requires explicit redaction state")
			}
			if file["redacted"] == true {
				return InsufficientEvidence, errors.New("preserved file content is redacted")
			}
			artifact, err := evidence.Resolve(snap.Evidence, models.EvidenceReference{Origin: snap.Evidence.Origin, ArtifactID: "workspace-file/" + path})
			if err != nil {
				return Invalid, err
			}
			if artifact.Kind != "workspace_file" || artifact.Document != "snapshot" ||
				artifact.Pointer != fmt.Sprintf("/workspaceFiles/%d", index) || artifact.ContentDigest.Encoding != "json-v1" {
				return Invalid, errors.New("preserved file kind or exact raw locator is inconsistent")
			}
		}
		if !found {
			return InsufficientEvidence, fmt.Errorf("required preserved workspace file %q is unavailable", path)
		}
	}
	return Observed, nil
}
