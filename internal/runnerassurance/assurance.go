package runnerassurance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Kind            = "waza.production-integration-assurance"
	Version         = "1.0"
	MaxPackageBytes = 1 << 20
	SchemaURI       = "https://microsoft.github.io/waza/schemas/production-integration-assurance-1.0.schema.json"
)

var ErrInvalid = errors.New("invalid production integration assurance package")

var expectedCapabilityStates = map[string]string{
	"provenance-local-signing":                    "supported",
	"provenance-keyless-signing":                  "supported",
	"provenance-managed-kms":                      "unavailable",
	"image-inventory-generation-verification":     "supported",
	"inventory-refresh-and-legacy-images":         "unavailable",
	"post-run-telemetry":                          "supported",
	"wall-time-and-activity-budgets":              "supported",
	"os-resource-budget-gaps":                     "unsupported",
	"off-host-network-and-remote-tools":           "supported",
	"filesystem-environment-subprocess-isolation": "unavailable",
	"adapter-translation":                         "supported",
	"adapter-scheduler-orchestration":             "unsupported",
	"adapter-provenance-hook":                     "supported",
}

type Package struct {
	Kind               string            `json:"kind"`
	SchemaVersion      string            `json:"schema_version"`
	PackageVersion     string            `json:"package_version"`
	TargetRelease      string            `json:"target_release"`
	ReleaseStage       string            `json:"release_stage"`
	Summary            string            `json:"summary"`
	ConformanceMatrix  []Capability      `json:"conformance_matrix"`
	ThreatModel        []Threat          `json:"threat_model"`
	ReadinessChecklist []ChecklistItem   `json:"readiness_checklist"`
	VerificationLimits []string          `json:"verification_limits"`
	LabEvidence        []LabEvidence     `json:"lab_evidence"`
	Migration          Migration         `json:"migration"`
	WorkflowEvidence   *WorkflowEvidence `json:"workflow_evidence,omitempty"`
}

type WorkflowEvidenceInput struct {
	Repository     string         `json:"repository"`
	SourceSHA      string         `json:"source_sha"`
	ReleaseStage   string         `json:"release_stage"`
	Workflow       string         `json:"workflow"`
	RunID          int64          `json:"run_id"`
	RunAttempt     int            `json:"run_attempt"`
	RunURL         string         `json:"run_url"`
	ArtifactName   string         `json:"artifact_name"`
	ArtifactID     int64          `json:"artifact_id"`
	ArtifactURL    string         `json:"artifact_url"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	Files          []EvidenceFile `json:"files"`
}

type WorkflowEvidence struct {
	Repository     string         `json:"repository"`
	SourceSHA      string         `json:"source_sha"`
	ReleaseStage   string         `json:"release_stage"`
	Workflow       string         `json:"workflow"`
	RunID          int64          `json:"run_id"`
	RunAttempt     int            `json:"run_attempt"`
	RunURL         string         `json:"run_url"`
	ArtifactName   string         `json:"artifact_name"`
	ArtifactID     int64          `json:"artifact_id"`
	ArtifactURL    string         `json:"artifact_url"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	Files          []EvidenceFile `json:"files"`
}

type EvidenceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Evidence struct {
	Label string `json:"label"`
	Path  string `json:"path"`
}

type Capability struct {
	ID           string     `json:"id"`
	Area         string     `json:"area"`
	State        string     `json:"state"`
	Claim        string     `json:"claim"`
	Verification string     `json:"verification"`
	Limitations  string     `json:"limitations"`
	Issues       []int      `json:"issues"`
	Schemas      []string   `json:"schemas"`
	Evidence     []Evidence `json:"evidence"`
}

type Threat struct {
	ID           string     `json:"id"`
	Boundary     string     `json:"boundary"`
	Threat       string     `json:"threat"`
	Controls     string     `json:"controls"`
	ResidualRisk string     `json:"residual_risk"`
	Evidence     []Evidence `json:"evidence"`
}

type ChecklistItem struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	Acceptance string     `json:"acceptance"`
	Evidence   []Evidence `json:"evidence"`
}

type LabEvidence struct {
	Issue int    `json:"issue"`
	Panel string `json:"panel"`
	Guide string `json:"guide"`
}

type Migration struct {
	Summary        string `json:"summary"`
	OldRunner      string `json:"old_runner"`
	PreviousImages string `json:"previous_images"`
	Reassessment   string `json:"reassessment"`
}

type Result struct {
	Package  Package
	JSON     []byte
	Markdown []byte
}

var packageSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ProductionIntegrationAssuranceSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("reading assurance package schema: %w", err)
	}
	if err := compiler.AddResource(SchemaURI, doc); err != nil {
		return nil, fmt.Errorf("registering assurance package schema: %w", err)
	}
	return compiler.Compile(SchemaURI)
})

func Build(data []byte, repoRoot string) (Result, error) {
	if len(data) == 0 || len(data) > MaxPackageBytes {
		return Result{}, fmt.Errorf("%w: package must contain 1 to %d bytes", ErrInvalid, MaxPackageBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return Result{}, fmt.Errorf("%w: parsing JSON: %w", ErrInvalid, err)
	}
	schema, err := packageSchema()
	if err != nil {
		return Result{}, fmt.Errorf("loading assurance package schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return Result{}, fmt.Errorf("%w: schema validation: %w", ErrInvalid, err)
	}

	var pkg Package
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pkg); err != nil {
		return Result{}, fmt.Errorf("%w: decoding package: %w", ErrInvalid, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("%w: package contains trailing data", ErrInvalid)
	}
	if err := validatePackage(pkg, repoRoot); err != nil {
		return Result{}, err
	}

	encoded, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encoding assurance package: %w", err)
	}
	encoded = append(encoded, '\n')
	return Result{Package: pkg, JSON: encoded, Markdown: render(pkg)}, nil
}

func BuildWithWorkflowEvidence(data []byte, repoRoot string, inputData []byte, evidenceRoot string) (Result, error) {
	result, err := Build(data, repoRoot)
	if err != nil {
		return Result{}, err
	}
	if len(inputData) == 0 || len(inputData) > 64<<10 {
		return Result{}, fmt.Errorf("%w: workflow evidence input must contain 1 to %d bytes", ErrInvalid, 64<<10)
	}
	var input WorkflowEvidenceInput
	decoder := json.NewDecoder(bytes.NewReader(inputData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return Result{}, fmt.Errorf("%w: decoding workflow evidence input: %w", ErrInvalid, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Result{}, fmt.Errorf("%w: workflow evidence input contains trailing data", ErrInvalid)
	}
	if err := validateWorkflowEvidenceInput(input); err != nil {
		return Result{}, err
	}
	files, err := hashEvidenceFiles(evidenceRoot)
	if err != nil {
		return Result{}, err
	}
	if err := compareEvidenceFiles(input.Files, files); err != nil {
		return Result{}, err
	}
	if err := requireQualificationFiles(files); err != nil {
		return Result{}, err
	}
	result.Package.ReleaseStage = input.ReleaseStage
	result.Package.WorkflowEvidence = &WorkflowEvidence{
		Repository: input.Repository, SourceSHA: input.SourceSHA, ReleaseStage: input.ReleaseStage,
		Workflow: input.Workflow, RunID: input.RunID, RunAttempt: input.RunAttempt, RunURL: input.RunURL,
		ArtifactName: input.ArtifactName, ArtifactID: input.ArtifactID, ArtifactURL: input.ArtifactURL,
		ArtifactSHA256: input.ArtifactSHA256, Files: files,
	}
	if err := validateWorkflowEvidence(*result.Package.WorkflowEvidence); err != nil {
		return Result{}, err
	}
	encoded, err := json.MarshalIndent(result.Package, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encoding qualification package: %w", err)
	}
	return Build(append(encoded, '\n'), repoRoot)
}

func validatePackage(pkg Package, repoRoot string) error {
	if pkg.Kind != Kind || pkg.SchemaVersion != Version || pkg.PackageVersion != Version {
		return fmt.Errorf("%w: unsupported kind or package version", ErrInvalid)
	}
	if pkg.TargetRelease != "v0.42.0" {
		return fmt.Errorf("%w: target_release must remain v0.42.0 for package 1.0", ErrInvalid)
	}
	if pkg.ReleaseStage != "candidate" && pkg.ReleaseStage != "released" {
		return fmt.Errorf("%w: release_stage must be candidate or released", ErrInvalid)
	}
	if pkg.WorkflowEvidence != nil {
		if pkg.WorkflowEvidence.ReleaseStage != pkg.ReleaseStage {
			return fmt.Errorf("%w: workflow evidence release stage differs from package stage", ErrInvalid)
		}
		if err := validateWorkflowEvidence(*pkg.WorkflowEvidence); err != nil {
			return err
		}
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return fmt.Errorf("resolving repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolving repository root: %w", err)
	}
	schemaRoute, err := readRepositoryFile(root, "site/src/pages/schemas/[schema].json.ts")
	if err != nil {
		return fmt.Errorf("reading published schema routes: %w", err)
	}
	if !strings.Contains(string(schemaRoute), "schema: 'production-integration-assurance-1.0.schema'") {
		return fmt.Errorf("%w: assurance package schema is not published by the documentation site", ErrInvalid)
	}
	seenCapabilities := make(map[string]bool, len(pkg.ConformanceMatrix))
	referencedSchemas := make(map[string]bool)
	states := map[string]bool{}
	for _, row := range pkg.ConformanceMatrix {
		if seenCapabilities[row.ID] {
			return fmt.Errorf("%w: duplicate conformance id %q", ErrInvalid, row.ID)
		}
		expectedState, knownCapability := expectedCapabilityStates[row.ID]
		if !knownCapability || row.State != expectedState {
			return fmt.Errorf("%w: conformance %q has an unknown capability or unsupported state %q", ErrInvalid, row.ID, row.State)
		}
		seenCapabilities[row.ID] = true
		states[row.State] = true
		for _, ref := range row.Schemas {
			if !strings.HasPrefix(ref, "schemas/") || !strings.HasSuffix(ref, ".schema.json") {
				return fmt.Errorf("%w: %q is not a versioned schema path", ErrInvalid, ref)
			}
			schemaBytes, err := readRepositoryFile(root, ref)
			if err != nil {
				return err
			}
			schemaDoc, err := jsonutil.Parse(schemaBytes)
			if err != nil {
				return fmt.Errorf("%w: schema reference %q is not valid JSON: %w", ErrInvalid, ref, err)
			}
			schemaObject, ok := schemaDoc.(map[string]any)
			if !ok || !strings.HasPrefix(stringValue(schemaObject["$id"]), "https://microsoft.github.io/waza/schemas/") {
				return fmt.Errorf("%w: schema reference %q has no published Waza schema id", ErrInvalid, ref)
			}
			schemaKey := strings.TrimSuffix(filepath.Base(ref), ".json")
			if !strings.Contains(string(schemaRoute), "schema: '"+schemaKey+"'") {
				return fmt.Errorf("%w: schema reference %q is not published by the documentation site", ErrInvalid, ref)
			}
			referencedSchemas[ref] = true
		}
		if err := validateEvidence(root, row.Evidence); err != nil {
			return fmt.Errorf("conformance %q: %w", row.ID, err)
		}
	}
	if len(seenCapabilities) != len(expectedCapabilityStates) {
		return fmt.Errorf("%w: conformance matrix must include every version 1.0 capability", ErrInvalid)
	}
	schemaRefs := make([]string, 0, len(referencedSchemas))
	for ref := range referencedSchemas {
		schemaRefs = append(schemaRefs, ref)
	}
	sort.Strings(schemaRefs)
	if err := validateReferencedSchemas(root, schemaRefs); err != nil {
		return err
	}
	if !states["supported"] || !states["unsupported"] || !states["unavailable"] {
		return fmt.Errorf("%w: conformance matrix must distinguish supported, unsupported, and unavailable states", ErrInvalid)
	}
	for _, row := range pkg.ThreatModel {
		if err := validateEvidence(root, row.Evidence); err != nil {
			return fmt.Errorf("threat %q: %w", row.ID, err)
		}
	}
	for _, item := range pkg.ReadinessChecklist {
		if err := validateEvidence(root, item.Evidence); err != nil {
			return fmt.Errorf("checklist %q: %w", item.ID, err)
		}
	}
	for _, limit := range pkg.VerificationLimits {
		if strings.TrimSpace(limit) == "" {
			return fmt.Errorf("%w: empty verification limit", ErrInvalid)
		}
	}

	seenLab := make(map[int]bool, len(pkg.LabEvidence))
	for _, item := range pkg.LabEvidence {
		if item.Issue < 769 || item.Issue > 774 || seenLab[item.Issue] {
			return fmt.Errorf("%w: Lab evidence must link each issue #769 through #774 exactly once", ErrInvalid)
		}
		if !strings.HasPrefix(item.Guide, "site/src/content/docs/guides/") || !strings.HasSuffix(item.Guide, ".mdx") {
			return fmt.Errorf("%w: Lab evidence guide %q must be a documentation-site guide", ErrInvalid, item.Guide)
		}
		seenLab[item.Issue] = true
		guide, err := readRepositoryFile(root, item.Guide)
		if err != nil {
			return fmt.Errorf("lab guide for issue #%d: %w", item.Issue, err)
		}
		if !strings.Contains(strings.ToLower(string(guide)), strings.ToLower(item.Panel)) {
			return fmt.Errorf("%w: Lab panel %q for issue #%d is not described by %s", ErrInvalid, item.Panel, item.Issue, item.Guide)
		}
	}
	for issue := 769; issue <= 774; issue++ {
		if !seenLab[issue] {
			return fmt.Errorf("%w: missing Lab evidence link for issue #%d", ErrInvalid, issue)
		}
	}
	return nil
}

func validateWorkflowEvidenceInput(input WorkflowEvidenceInput) error {
	if !repositoryPattern.MatchString(input.Repository) {
		return fmt.Errorf("%w: invalid workflow evidence repository", ErrInvalid)
	}
	if !commitPattern.MatchString(input.SourceSHA) {
		return fmt.Errorf("%w: workflow evidence source must be a full commit SHA", ErrInvalid)
	}
	if input.ReleaseStage != "candidate" && input.ReleaseStage != "released" {
		return fmt.Errorf("%w: workflow evidence release stage must be candidate or released", ErrInvalid)
	}
	if strings.TrimSpace(input.Workflow) == "" || input.RunID <= 0 || input.RunAttempt <= 0 || input.ArtifactID <= 0 {
		return fmt.Errorf("%w: workflow and run/artifact identities are required", ErrInvalid)
	}
	if strings.TrimSpace(input.ArtifactName) == "" || !sha256Pattern.MatchString(input.ArtifactSHA256) {
		return fmt.Errorf("%w: artifact name or digest is invalid", ErrInvalid)
	}
	if err := validateEvidenceFiles(input.Files); err != nil {
		return fmt.Errorf("%w: workflow evidence file digests are invalid", ErrInvalid)
	}
	if err := validateGitHubRunURL(input.Repository, input.RunID, 0, input.RunURL, false); err != nil {
		return err
	}
	return validateGitHubRunURL(input.Repository, input.RunID, input.ArtifactID, input.ArtifactURL, true)
}

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	commitPattern     = regexp.MustCompile(`^[a-f0-9]{40}$`)
	sha256Pattern     = regexp.MustCompile(`^(?:sha256:)?[a-f0-9]{64}$`)
)

func validateWorkflowEvidence(evidence WorkflowEvidence) error {
	if err := validateWorkflowEvidenceInput(WorkflowEvidenceInput(evidence)); err != nil {
		return err
	}
	return nil
}

func validateEvidenceFiles(files []EvidenceFile) error {
	if len(files) == 0 || len(files) > 1024 {
		return fmt.Errorf("%w: qualification workflow evidence must list 1 to 1024 files", ErrInvalid)
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		if file.Path == "" || path.IsAbs(file.Path) || path.Clean(file.Path) != file.Path ||
			strings.HasPrefix(file.Path, "../") || file.Path == ".." || strings.Contains(file.Path, "\\") ||
			!sha256Pattern.MatchString(file.SHA256) {
			return fmt.Errorf("%w: invalid qualification evidence file %q", ErrInvalid, file.Path)
		}
		if seen[file.Path] {
			return fmt.Errorf("%w: duplicate qualification evidence file %q", ErrInvalid, file.Path)
		}
		seen[file.Path] = true
	}
	return nil
}

func compareEvidenceFiles(expected, actual []EvidenceFile) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("%w: workflow artifact file list is incomplete or unexpected", ErrInvalid)
	}
	byPath := make(map[string]string, len(expected))
	for _, file := range expected {
		if file.Path == "" || !sha256Pattern.MatchString(file.SHA256) || byPath[file.Path] != "" {
			return fmt.Errorf("%w: workflow evidence file list has an invalid or duplicate entry", ErrInvalid)
		}
		byPath[file.Path] = strings.TrimPrefix(file.SHA256, "sha256:")
	}
	for _, file := range actual {
		if byPath[file.Path] != file.SHA256 {
			return fmt.Errorf("%w: workflow artifact file %q is missing, unexpected, or tampered", ErrInvalid, file.Path)
		}
		delete(byPath, file.Path)
	}
	if len(byPath) != 0 {
		return fmt.Errorf("%w: workflow artifact file list is incomplete", ErrInvalid)
	}
	return nil
}

func validateGitHubRunURL(repository string, runID, artifactID int64, raw string, artifact bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: workflow evidence URL must be an HTTPS GitHub link", ErrInvalid)
	}
	expected := fmt.Sprintf("/%s/actions/runs/%d", repository, runID)
	if artifact {
		expected += "/artifacts/"
		pathArtifactID := strings.TrimPrefix(parsed.Path, expected)
		if !strings.HasPrefix(parsed.Path, expected) || !artifactIDPattern.MatchString(pathArtifactID) ||
			pathArtifactID != strconv.FormatInt(artifactID, 10) {
			return fmt.Errorf("%w: workflow evidence artifact URL does not identify this run and artifact", ErrInvalid)
		}
	} else if parsed.Path != expected {
		return fmt.Errorf("%w: workflow evidence run URL does not identify this run", ErrInvalid)
	}
	return nil
}

var artifactIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

func hashEvidenceFiles(root string) ([]EvidenceFile, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolving qualification evidence root: %w", ErrInvalid, err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: qualification evidence root must be a real directory", ErrInvalid)
	}
	var files []EvidenceFile
	var totalBytes int64
	err = filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in qualification evidence: %s", name)
		}
		if entry.IsDir() {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil || !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("qualification evidence is not a regular file: %s", name)
		}
		totalBytes += fileInfo.Size()
		if len(files) >= 1024 || totalBytes > 5<<30 {
			return fmt.Errorf("qualification evidence exceeds file or size limit")
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		files = append(files, EvidenceFile{
			Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(hash.Sum(nil)),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: hashing qualification workflow outputs: %w", ErrInvalid, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func requireQualificationFiles(files []EvidenceFile) error {
	required := []string{
		"contract-conformance.json", "offline-verification.json", "runner-image.oci.tar",
		"runner-image.archive.sha256", "runner-image.config.digest", "runner-image.digest",
		"runner-image.manifest.json", "runner-inventory.cdx.json", "trusted_root.jsonl",
		"attestations/binaries.json", "attestations/runner-image-archive.json",
		"attestations/runner-image.json", "attestations/runner-inventory.json",
		"execution-provenance/statement.json", "execution-provenance/envelope.json",
		"execution-provenance/verification.json", "execution-provenance/public-key.pem",
		"execution-provenance/bundle/bundle.json", "execution-provenance/bundle/statement.dsse.json",
		"verification/attestations/runner-image-archive-provenance.json",
		"verification/attestations/runner-image-provenance.json",
		"verification/attestations/runner-inventory-sbom.json",
		"verification/runner-inventory.json",
	}
	binaries := []string{
		"waza-linux-amd64", "waza-linux-arm64", "waza-darwin-amd64",
		"waza-darwin-arm64", "waza-windows-amd64.exe", "waza-windows-arm64.exe",
	}
	for _, binary := range binaries {
		required = append(required, binary, binary+".source-sha.txt", binary+".sha256",
			"verification/attestations/"+binary+".json")
	}
	present := make(map[string]bool, len(files))
	for _, file := range files {
		present[file.Path] = true
	}
	for _, name := range required {
		if !present[name] {
			return fmt.Errorf("%w: qualification workflow output %q is missing", ErrInvalid, name)
		}
	}
	return nil
}

func validateReferencedSchemas(root string, references []string) error {
	entries, err := os.ReadDir(filepath.Join(root, "schemas"))
	if err != nil {
		return fmt.Errorf("reading local contract schemas: %w", err)
	}
	resources := make(map[string][]byte)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".schema.json") {
			continue
		}
		ref := path.Join("schemas", entry.Name())
		content, err := readRepositoryFile(root, ref)
		if err != nil {
			return err
		}
		resources[ref] = content
	}
	return compileSchemaResources(resources, references)
}

func compileSchemaResources(resources map[string][]byte, references []string) error {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	resourcePaths := make([]string, 0, len(resources))
	for ref := range resources {
		resourcePaths = append(resourcePaths, ref)
	}
	sort.Strings(resourcePaths)
	resourceIDs := make(map[string]string, len(resources))
	for _, ref := range resourcePaths {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(resources[ref]))
		if err != nil {
			return fmt.Errorf("%w: parsing local contract schema %q: %w", ErrInvalid, ref, err)
		}
		schemaObject, ok := doc.(map[string]any)
		if !ok {
			return fmt.Errorf("%w: local contract schema %q is not an object", ErrInvalid, ref)
		}
		id := stringValue(schemaObject["$id"])
		if !strings.HasPrefix(id, "https://microsoft.github.io/waza/schemas/") {
			continue
		}
		if err := compiler.AddResource(id, doc); err != nil {
			return fmt.Errorf("%w: registering local contract schema %q: %w", ErrInvalid, ref, err)
		}
		resourceIDs[ref] = id
	}
	for _, ref := range references {
		id, ok := resourceIDs[ref]
		if !ok {
			return fmt.Errorf("%w: referenced contract schema %q has no registered Waza schema id", ErrInvalid, ref)
		}
		if _, err := compiler.Compile(id); err != nil {
			return fmt.Errorf("%w: compiling contract schema %q: %w", ErrInvalid, ref, err)
		}
	}
	return nil
}

func validateEvidence(root string, evidence []Evidence) error {
	if len(evidence) == 0 {
		return fmt.Errorf("%w: every claim requires evidence", ErrInvalid)
	}
	for _, item := range evidence {
		if strings.TrimSpace(item.Label) == "" {
			return fmt.Errorf("%w: evidence label is empty", ErrInvalid)
		}
		if _, err := readRepositoryFile(root, item.Path); err != nil {
			return err
		}
	}
	return nil
}

func readRepositoryFile(root, ref string) ([]byte, error) {
	if ref == "" || strings.Contains(ref, "\\") || path.IsAbs(ref) || path.Clean(ref) != ref || strings.HasPrefix(ref, "../") {
		return nil, fmt.Errorf("%w: unsafe repository evidence path %q", ErrInvalid, ref)
	}
	candidate := filepath.Join(root, filepath.FromSlash(ref))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, fmt.Errorf("%w: evidence %q is missing: %w", ErrInvalid, ref, err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: evidence %q resolves outside the repository", ErrInvalid, ref)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: reading evidence %q: %w", ErrInvalid, ref, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: evidence %q is not a regular file", ErrInvalid, ref)
	}
	return os.ReadFile(resolved)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func render(pkg Package) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "---\ntitle: Production integration assurance package\ndescription: Versioned, evidence-linked production integration conformance and readiness guidance for %s.\n---\n\n", pkg.TargetRelease)
	fmt.Fprintf(&out, "> **Package %s · %s · %s.** %s\n\n", pkg.PackageVersion, pkg.TargetRelease, pkg.ReleaseStage, pkg.Summary)
	out.WriteString("This is an operator qualification aid, not a blanket production-readiness guarantee or a substitute for deployment-specific risk assessment. Evidence for the #769–#774 contracts is linked below; this package adds **no new Lab dashboard view**.\n\n")
	if pkg.WorkflowEvidence == nil {
		fmt.Fprintf(&out, "Source evidence links are pinned to the `%s` release tag. The source package remains a candidate until tag-release qualification completes; its offline builder verifies each evidence file against the checked-out source.\n\n", pkg.TargetRelease)
	} else {
		renderWorkflowEvidence(&out, *pkg.WorkflowEvidence)
	}
	out.WriteString("**Contents:** [Conformance matrix](#operator-conformance-matrix) · [Threat model](#threat-model) · [Readiness checklist](#production-readiness-checklist) · [Verification limits](#verification-limits) · [Migration and reassessment](#migration-and-reassessment) · [Existing Lab evidence](#existing-lab-evidence)\n\n")

	revision := pkg.TargetRelease
	if pkg.WorkflowEvidence != nil {
		revision = pkg.WorkflowEvidence.SourceSHA
	}
	out.WriteString("## Operator conformance matrix\n\n")
	out.WriteString("| Capability | State | Claim and verification | Declared limitations | Evidence |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range pkg.ConformanceMatrix {
		issues := make([]string, len(row.Issues))
		for i, issue := range row.Issues {
			issues[i] = fmt.Sprintf("[#%d](https://github.com/microsoft/waza/issues/%d)", issue, issue)
		}
		evidence := renderEvidence(row.Evidence, revision) + "<br />Contracts: " + renderSchemas(row.Schemas)
		fmt.Fprintf(&out, "| **%s** (%s)<br />`%s` | `%s` | %s %s | %s | %s |\n",
			escapeCell(row.Area), strings.Join(issues, ", "), escapeCell(row.ID), row.State,
			escapeCell(row.Claim), escapeCell(row.Verification), escapeCell(row.Limitations), evidence)
	}

	out.WriteString("\n## Threat model\n\n")
	out.WriteString("| Boundary | Threat | Current controls | Residual risk | Evidence |\n| --- | --- | --- | --- | --- |\n")
	for _, threat := range pkg.ThreatModel {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n",
			escapeCell(threat.Boundary), escapeCell(threat.Threat), escapeCell(threat.Controls),
			escapeCell(threat.ResidualRisk), renderEvidence(threat.Evidence, revision))
	}

	out.WriteString("\n## Production-readiness checklist\n\n")
	for _, item := range pkg.ReadinessChecklist {
		fmt.Fprintf(&out, "- [ ] **%s** — %s %s Evidence: %s\n",
			escapeCell(item.Action), escapeCell(item.Acceptance), fmt.Sprintf("`%s`.", item.ID), renderEvidence(item.Evidence, revision))
	}

	out.WriteString("\n## Verification limits\n\n")
	for _, limit := range pkg.VerificationLimits {
		fmt.Fprintf(&out, "- %s\n", limit)
	}

	out.WriteString("\n## Migration and reassessment\n\n")
	fmt.Fprintf(&out, "- **Migration:** %s\n", pkg.Migration.Summary)
	fmt.Fprintf(&out, "- **Older runners:** %s\n", pkg.Migration.OldRunner)
	fmt.Fprintf(&out, "- **Previously published images:** %s\n", pkg.Migration.PreviousImages)
	fmt.Fprintf(&out, "- **After upgrades:** %s\n", pkg.Migration.Reassessment)

	out.WriteString("\n## Existing Lab evidence\n\n")
	out.WriteString("The following existing evidence views are linked rather than duplicated. Use the [Dashboard Lab guide](../dashboard-lab/) for navigation and local Lab setup.\n\n")
	out.WriteString("| Issue | Existing Lab panel | Operator guide |\n| --- | --- | --- |\n")
	for _, item := range pkg.LabEvidence {
		fmt.Fprintf(&out, "| [#%d](https://github.com/microsoft/waza/issues/%d) | %s | [%s](%s) |\n",
			item.Issue, item.Issue, escapeCell(item.Panel), escapeCell(item.Panel), siteGuidePath(item.Guide))
	}
	out.WriteString("\nMaintainers: update the versioned source package at [`docs/PRODUCTION_INTEGRATION_ASSURANCE-1.0.json`](https://github.com/microsoft/waza/blob/main/docs/PRODUCTION_INTEGRATION_ASSURANCE-1.0.json), then run `go run ./cmd/waza runner assurance build --format markdown` from the repository root and commit the regenerated page with the source change.\n")
	return []byte(out.String())
}

func renderWorkflowEvidence(out *strings.Builder, evidence WorkflowEvidence) {
	fmt.Fprintf(out, "**Qualification workflow:** [%s](%s) · source [`%s`](https://github.com/%s/commit/%s) · release stage `%s`.\n\n",
		escapeCell(evidence.Workflow), evidence.RunURL, evidence.SourceSHA, evidence.Repository, evidence.SourceSHA, evidence.ReleaseStage)
	fmt.Fprintf(out, "The exact workflow-run artifact [%s](%s) is bound to SHA-256 `%s`; its files and digests were derived from the downloaded artifact, and the workflow verified the artifact record before generating this package.\n\n",
		escapeCell(evidence.ArtifactName), evidence.ArtifactURL, strings.TrimPrefix(evidence.ArtifactSHA256, "sha256:"))
	out.WriteString("### Workflow output digests\n\n| File | SHA-256 |\n| --- | --- |\n")
	for _, file := range evidence.Files {
		fmt.Fprintf(out, "| [%s](%s) | `%s` |\n", escapeCell(file.Path), evidence.ArtifactURL, file.SHA256)
	}
	out.WriteByte('\n')
}

func renderEvidence(evidence []Evidence, revision string) string {
	links := make([]string, len(evidence))
	for i, item := range evidence {
		links[i] = fmt.Sprintf("[%s](https://github.com/microsoft/waza/blob/%s/%s)", escapeCell(item.Label), revision, url.PathEscape(item.Path))
		links[i] = strings.ReplaceAll(links[i], "%2F", "/")
	}
	return strings.Join(links, "<br />")
}

func renderSchemas(refs []string) string {
	links := make([]string, len(refs))
	for i, ref := range refs {
		name := path.Base(ref)
		href := "https://microsoft.github.io/waza/schemas/" + url.PathEscape(name)
		links[i] = fmt.Sprintf("[%s](%s)", name, href)
	}
	return strings.Join(links, ", ")
}

func siteGuidePath(ref string) string {
	const prefix = "site/src/content/docs/guides/"
	if !strings.HasPrefix(ref, prefix) {
		return "./"
	}
	name := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), ".mdx")
	return "../" + name + "/"
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func FindRepositoryRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolving repository search path: %w", err)
	}
	for {
		if regularFile(filepath.Join(current, "go.mod")) &&
			regularFile(filepath.Join(current, "docs", "PRODUCTION_INTEGRATION_ASSURANCE-1.0.json")) &&
			regularFile(filepath.Join(current, "schemas", "production-integration-assurance-1.0.schema.json")) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("cannot find Waza repository root; run this command from a source checkout")
		}
		current = parent
	}
}

func regularFile(name string) bool {
	info, err := os.Stat(name)
	return err == nil && info.Mode().IsRegular()
}
