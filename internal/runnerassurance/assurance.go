package runnerassurance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
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
	"provenance-keyless-kms":                      "unavailable",
	"image-inventory-generation-verification":     "supported",
	"inventory-refresh-and-legacy-images":         "unavailable",
	"post-run-telemetry":                          "supported",
	"wall-time-and-activity-budgets":              "supported",
	"os-resource-budget-gaps":                     "unsupported",
	"off-host-network-and-remote-tools":           "supported",
	"filesystem-environment-subprocess-isolation": "unavailable",
	"adapter-translation":                         "supported",
	"adapter-scheduler-and-provenance-hook":       "unsupported",
}

type Package struct {
	Kind               string          `json:"kind"`
	SchemaVersion      string          `json:"schema_version"`
	PackageVersion     string          `json:"package_version"`
	TargetRelease      string          `json:"target_release"`
	ReleaseStage       string          `json:"release_stage"`
	Summary            string          `json:"summary"`
	ConformanceMatrix  []Capability    `json:"conformance_matrix"`
	ThreatModel        []Threat        `json:"threat_model"`
	ReadinessChecklist []ChecklistItem `json:"readiness_checklist"`
	VerificationLimits []string        `json:"verification_limits"`
	LabEvidence        []LabEvidence   `json:"lab_evidence"`
	Migration          Migration       `json:"migration"`
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
	fmt.Fprintf(&out, "Evidence links are pinned to the `%s` release tag. While this package is a candidate, those links resolve after the tag is published; the offline builder verifies each evidence file against the local checkout.\n\n", pkg.TargetRelease)
	out.WriteString("**Contents:** [Conformance matrix](#operator-conformance-matrix) · [Threat model](#threat-model) · [Readiness checklist](#production-readiness-checklist) · [Verification limits](#verification-limits) · [Migration and reassessment](#migration-and-reassessment) · [Existing Lab evidence](#existing-lab-evidence)\n\n")

	out.WriteString("## Operator conformance matrix\n\n")
	out.WriteString("| Capability | State | Claim and verification | Declared limitations | Evidence |\n| --- | --- | --- | --- | --- |\n")
	for _, row := range pkg.ConformanceMatrix {
		issues := make([]string, len(row.Issues))
		for i, issue := range row.Issues {
			issues[i] = fmt.Sprintf("[#%d](https://github.com/microsoft/waza/issues/%d)", issue, issue)
		}
		evidence := renderEvidence(row.Evidence, pkg.TargetRelease) + "<br />Contracts: " + renderSchemas(row.Schemas)
		fmt.Fprintf(&out, "| **%s** (%s)<br />`%s` | `%s` | %s %s | %s | %s |\n",
			escapeCell(row.Area), strings.Join(issues, ", "), escapeCell(row.ID), row.State,
			escapeCell(row.Claim), escapeCell(row.Verification), escapeCell(row.Limitations), evidence)
	}

	out.WriteString("\n## Threat model\n\n")
	out.WriteString("| Boundary | Threat | Current controls | Residual risk | Evidence |\n| --- | --- | --- | --- | --- |\n")
	for _, threat := range pkg.ThreatModel {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n",
			escapeCell(threat.Boundary), escapeCell(threat.Threat), escapeCell(threat.Controls),
			escapeCell(threat.ResidualRisk), renderEvidence(threat.Evidence, pkg.TargetRelease))
	}

	out.WriteString("\n## Production-readiness checklist\n\n")
	for _, item := range pkg.ReadinessChecklist {
		fmt.Fprintf(&out, "- [ ] **%s** — %s %s Evidence: %s\n",
			escapeCell(item.Action), escapeCell(item.Acceptance), fmt.Sprintf("`%s`.", item.ID), renderEvidence(item.Evidence, pkg.TargetRelease))
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
