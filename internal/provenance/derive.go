package provenance

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

// Failure is one precise, stable verification or generation failure.
type Failure struct {
	Check   string
	Code    string
	Message string
}

func (f *Failure) Error() string { return f.Check + ": " + f.Code + ": " + f.Message }

func fail(check, code, format string, args ...any) *Failure {
	return &Failure{Check: check, Code: code, Message: fmt.Sprintf(format, args...)}
}

var executionModes = map[string]string{"mock": "mock", "copilot-sdk": "copilot"}

type inputClaims struct {
	inputs               InputsClaim
	apiVersion           string
	requestSchemaVersion string
	runtime              RuntimeClaim
	// admitted is the caller binding runner admission derives from the
	// request; the manifest and terminal must match it exactly.
	admitted artifactmanifest.Binding
}

// deriveInputs recomputes the canonical identity, negotiated runner API
// version and runtime/model identity from the captured request bytes.
func deriveInputs(request []byte) (*inputClaims, *Failure) {
	parsed, err := runnercontract.Parse(request)
	if err != nil {
		return nil, fail("inputs", "input_invalid", "runner request is invalid: %v", err)
	}
	identity, err := runneridentity.DeriveValidated(parsed)
	if err != nil {
		return nil, fail("inputs", "input_identity_invalid", "canonical identity cannot be derived: %v", err)
	}
	negotiation := runnercontract.NegotiateValidatedRequest(parsed)
	if negotiation.Status != "compatible" || negotiation.Negotiation.Version == "" {
		return nil, fail("runner", "runner_api_incompatible", "runner request does not negotiate a supported API version (%s)", negotiation.Status)
	}
	spec, err := models.ParseCapturedEvalSpecOffline([]byte(parsed.Input.Eval), "eval.yaml")
	if err != nil {
		return nil, fail("runtime", "runtime_unavailable", "eval configuration cannot be parsed: %v", err)
	}
	mode, ok := executionModes[spec.Config.EngineType]
	if !ok {
		return nil, fail("runtime", "runtime_unavailable", "config.executor %q has no runner execution mode", spec.Config.EngineType)
	}
	// The runner executes config.model unchanged, so the claim keeps it verbatim.
	model := spec.Config.ModelID
	if strings.TrimSpace(model) == "" {
		return nil, fail("runtime", "runtime_model_unavailable", "config.model is blank; a model identity is required")
	}
	admitted, ok := runnerunit.AdmittedBinding(request)
	if !ok || admitted.Identity != *identity {
		return nil, fail("inputs", "input_not_admitted", "runner request is not admitted by the runner unit contract")
	}
	return &inputClaims{
		admitted:             admitted,
		inputs:               InputsClaim{Request: Ref{Path: RequestPath, SHA256: sha256Hex(request), MediaType: "application/json"}, Identity: *identity},
		apiVersion:           negotiation.Negotiation.Version,
		requestSchemaVersion: parsed.SchemaVersion,
		runtime:              RuntimeClaim{ExecutionMode: mode, Executor: spec.Config.EngineType, Model: model},
	}, nil
}

type artifactClaims struct {
	artifacts ArtifactsClaim
	outcome   OutcomeClaim
	mode      string
}

// deriveArtifacts verifies the canonical manifest, every declared object and
// the terminal envelope, and binds them to the binding admitted from the
// request (identity, selection, execution mode and grader IDs).
func deriveArtifacts(manifest []byte, objects map[string][]byte, admitted artifactmanifest.Binding) (*artifactClaims, *Failure) {
	m, err := artifactmanifest.Parse(manifest)
	if err != nil {
		return nil, fail("artifacts", "manifest_invalid", "artifact manifest is invalid: %v", err)
	}
	canonical, err := artifactmanifest.Encode(*m)
	if err != nil || !bytes.Equal(canonical, bytes.TrimSuffix(manifest, []byte("\n"))) {
		return nil, fail("artifacts", "manifest_noncanonical", "artifact manifest is not in canonical form")
	}
	if m.Binding.Identity != admitted.Identity {
		return nil, fail("artifacts", "manifest_identity_mismatch", "artifact manifest identity does not match the canonical input identity")
	}
	if m.Binding.Selection != admitted.Selection || m.Binding.ExecutionMode != admitted.ExecutionMode || !slices.Equal(m.Binding.GraderIDs, admitted.GraderIDs) {
		return nil, fail("artifacts", "manifest_binding_mismatch", "artifact manifest selection, execution mode or grader IDs do not match the admitted request")
	}
	claims := &artifactClaims{
		artifacts: ArtifactsClaim{Manifest: Ref{Path: ManifestPath, SHA256: sha256Hex(manifest), MediaType: "application/json"}},
		mode:      m.Binding.ExecutionMode,
	}
	for _, a := range m.Artifacts {
		data, ok := objects[a.Name]
		if !ok {
			return nil, fail("artifacts", "artifact_missing", "artifact %q is declared but not present", a.Name)
		}
		if int64(len(data)) != a.SizeBytes || sha256Hex(data) != a.SHA256 {
			return nil, fail("artifacts", "artifact_digest_mismatch", "artifact %q does not match its manifest digest", a.Name)
		}
		claims.artifacts.Objects = append(claims.artifacts.Objects, Ref{Path: ObjectsDir + "/" + a.Name, SHA256: a.SHA256, MediaType: a.MediaType})
	}
	for name := range objects {
		if !slices.ContainsFunc(m.Artifacts, func(a artifactmanifest.Artifact) bool { return a.Name == name }) {
			return nil, fail("artifacts", "artifact_undeclared", "artifact %q is not declared in the manifest", name)
		}
	}
	terminal := objects[m.TerminalArtifact]
	envelope, err := runnerterminal.Validate(terminal, admitted.TerminalBinding())
	if err != nil {
		return nil, fail("outcome", "terminal_invalid", "terminal outcome is invalid or inconsistent with the manifest: %v", err)
	}
	if envelope.Identity != nil && *envelope.Identity != admitted.Identity {
		return nil, fail("outcome", "terminal_identity_mismatch", "terminal identity does not match the canonical input identity")
	}
	claims.outcome = OutcomeClaim{
		Artifact:        Ref{Path: ObjectsDir + "/" + m.TerminalArtifact, SHA256: sha256Hex(terminal), MediaType: "application/json"},
		Outcome:         string(envelope.Outcome),
		ExitCode:        envelope.ExitCode,
		FailureBoundary: envelope.FailureBoundary,
	}
	return claims, nil
}
