package checks

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/skill"
)

// allowedSpecFields lists the top-level frontmatter keys permitted by the agentskills.io spec.
var allowedSpecFields = map[string]bool{
	"name":          true,
	"description":   true,
	"license":       true,
	"allowed-tools": true,
	"metadata":      true,
	"compatibility": true,
}

// specNamePattern validates the spec name format: lowercase alphanumeric + hyphens,
// no leading/trailing hyphens, no consecutive --.
var specNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ScoreCheckData carries the status and optional evidence for score-style checks.
type ScoreCheckData struct {
	Status   CheckStatus
	Evidence string
}

// GetStatus implements StatusHolder.
func (d *ScoreCheckData) GetStatus() CheckStatus { return d.Status }

// SpecFrontmatterChecker validates that the file has YAML frontmatter with required fields.
type SpecFrontmatterChecker struct{}

var _ ComplianceChecker = (*SpecFrontmatterChecker)(nil)

func (*SpecFrontmatterChecker) Name() string { return "spec-frontmatter" }

func (*SpecFrontmatterChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-frontmatter",
			Passed:  false,
			Summary: "YAML frontmatter is missing",
			Data:    &ScoreCheckData{Status: StatusWarning},
		}, nil
	}
	var missing []string
	if sk.Frontmatter.Name == "" {
		missing = append(missing, "name")
	}
	if strings.TrimSpace(sk.Frontmatter.Description) == "" {
		missing = append(missing, "description")
	}
	if len(missing) > 0 {
		return &CheckResult{
			Name:    "spec-frontmatter",
			Passed:  false,
			Summary: fmt.Sprintf("Required frontmatter fields missing: %s", strings.Join(missing, ", ")),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec requires name and description"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-frontmatter",
		Passed:  true,
		Summary: "Frontmatter structure valid with required fields",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecAllowedFieldsChecker ensures all top-level frontmatter keys are spec-allowed.
type SpecAllowedFieldsChecker struct{}

var _ ComplianceChecker = (*SpecAllowedFieldsChecker)(nil)

func (*SpecAllowedFieldsChecker) Name() string { return "spec-allowed-fields" }

func (*SpecAllowedFieldsChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-allowed-fields",
			Passed:  true,
			Summary: "No frontmatter to validate",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}
	var unknown []string
	for key := range sk.FrontmatterRaw {
		if !allowedSpecFields[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return &CheckResult{
			Name:    "spec-allowed-fields",
			Passed:  false,
			Summary: fmt.Sprintf("Unknown frontmatter fields: %s", strings.Join(unknown, ", ")),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec allows: name, description, license, allowed-tools, metadata, compatibility"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-allowed-fields",
		Passed:  true,
		Summary: "All frontmatter fields are spec-allowed",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecNameChecker validates the name field against the spec's naming rules.
type SpecNameChecker struct{}

var _ ComplianceChecker = (*SpecNameChecker)(nil)

func (*SpecNameChecker) Name() string { return "spec-name" }

func (*SpecNameChecker) Check(sk skill.Skill) (*CheckResult, error) {
	name := sk.Frontmatter.Name
	if name == "" {
		return &CheckResult{
			Name:    "spec-name",
			Passed:  true,
			Summary: "No name to validate (caught by spec-frontmatter)",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}

	var violations []string
	if len(name) > 64 {
		violations = append(violations, fmt.Sprintf("exceeds 64 characters (%d)", len(name)))
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		violations = append(violations, "starts or ends with a hyphen")
	}
	if strings.Contains(name, "--") {
		violations = append(violations, "contains consecutive hyphens")
	}
	if !specNamePattern.MatchString(name) && len(violations) == 0 {
		violations = append(violations, "contains invalid characters (only lowercase letters, digits, and hyphens allowed)")
	}

	if len(violations) > 0 {
		return &CheckResult{
			Name:    "spec-name",
			Passed:  false,
			Summary: fmt.Sprintf("Name violates spec rules: %s", strings.Join(violations, "; ")),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: ≤64 chars, lowercase+digits+hyphens, no leading/trailing/consecutive hyphens"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-name",
		Passed:  true,
		Summary: "Name follows spec naming rules",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecDirMatchChecker checks that the skill directory's basename matches the name field.
type SpecDirMatchChecker struct{}

var _ ComplianceChecker = (*SpecDirMatchChecker)(nil)

func (*SpecDirMatchChecker) Name() string { return "spec-dir-match" }

func (*SpecDirMatchChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.Path == "" || sk.Frontmatter.Name == "" {
		return &CheckResult{
			Name:    "spec-dir-match",
			Passed:  true,
			Summary: "Cannot validate (missing path or name)",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}
	dir := filepath.Base(filepath.Dir(sk.Path))
	if dir != sk.Frontmatter.Name {
		return &CheckResult{
			Name:    "spec-dir-match",
			Passed:  false,
			Summary: fmt.Sprintf("Directory %q does not match skill name %q", dir, sk.Frontmatter.Name),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: directory basename must match name field"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-dir-match",
		Passed:  true,
		Summary: "Directory name matches skill name",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecDescriptionChecker validates the description field.
type SpecDescriptionChecker struct{}

var _ ComplianceChecker = (*SpecDescriptionChecker)(nil)

func (*SpecDescriptionChecker) Name() string { return "spec-description" }

func (*SpecDescriptionChecker) Check(sk skill.Skill) (*CheckResult, error) {
	desc := strings.TrimSpace(sk.Frontmatter.Description)
	if desc == "" {
		return &CheckResult{
			Name:    "spec-description",
			Passed:  false,
			Summary: "Description is empty",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: description must be non-empty"},
		}, nil
	}
	length := utf8.RuneCountInString(desc)
	if length > 1024 {
		return &CheckResult{
			Name:    "spec-description",
			Passed:  false,
			Summary: fmt.Sprintf("Description is %d characters (max 1024)", length),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: description must not exceed 1024 characters"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-description",
		Passed:  true,
		Summary: "Description is valid",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecCompatibilityChecker validates the optional compatibility field.
type SpecCompatibilityChecker struct{}

var _ ComplianceChecker = (*SpecCompatibilityChecker)(nil)

func (*SpecCompatibilityChecker) Name() string { return "spec-compatibility" }

func (*SpecCompatibilityChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-compatibility",
			Passed:  true,
			Summary: "No compatibility field (optional)",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}
	val, ok := sk.FrontmatterRaw["compatibility"]
	if !ok {
		return &CheckResult{
			Name:    "spec-compatibility",
			Passed:  true,
			Summary: "No compatibility field (optional)",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}
	m, isMap := val.(map[string]any)
	if !isMap {
		return &CheckResult{
			Name:    "spec-compatibility",
			Passed:  false,
			Summary: "Compatibility field must be a map",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: compatibility must be a mapping (e.g., editors, platforms)"},
		}, nil
	}
	for k, v := range m {
		if _, ok := v.(string); !ok {
			return &CheckResult{
				Name:    "spec-compatibility",
				Passed:  false,
				Summary: fmt.Sprintf("Compatibility key %q has non-string value", k),
				Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "agentskills.io spec: compatibility map values must be strings"},
			}, nil
		}
	}
	return &CheckResult{
		Name:    "spec-compatibility",
		Passed:  true,
		Summary: "Compatibility field is valid",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecLicenseChecker recommends including a license field.
type SpecLicenseChecker struct{}

var _ ComplianceChecker = (*SpecLicenseChecker)(nil)

func (*SpecLicenseChecker) Name() string { return "spec-license" }

func (*SpecLicenseChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-license",
			Passed:  true,
			Summary: "No license field found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include a license field (e.g., MIT, Apache-2.0)"},
		}, nil
	}
	val, ok := sk.FrontmatterRaw["license"]
	if !ok {
		return &CheckResult{
			Name:    "spec-license",
			Passed:  true,
			Summary: "No license field found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include a license field (e.g., MIT, Apache-2.0)"},
		}, nil
	}
	s, isStr := val.(string)
	if isStr && strings.TrimSpace(s) == "" {
		return &CheckResult{
			Name:    "spec-license",
			Passed:  true,
			Summary: "License field is empty",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include a non-empty license value"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-license",
		Passed:  true,
		Summary: "License field present",
		Data:    &ScoreCheckData{Status: StatusOptimal},
	}, nil
}

// SpecSecurityChecker flags security risks in frontmatter string values.
type SpecSecurityChecker struct{}

var _ ComplianceChecker = (*SpecSecurityChecker)(nil)

func (*SpecSecurityChecker) Name() string { return "spec-security" }

func (*SpecSecurityChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-security",
			Passed:  true,
			Summary: "No frontmatter to validate",
			Data:    &ScoreCheckData{Status: StatusOK},
		}, nil
	}

	var violations []string

	// Check all string values in frontmatter for XML angle brackets
	checkStringValue := func(key string, val any) {
		s, isStr := val.(string)
		if !isStr {
			return
		}
		if strings.ContainsAny(s, "<>") {
			violations = append(violations, fmt.Sprintf("%s contains XML angle brackets", key))
		}
	}

	// Recursively check frontmatter fields
	var checkMap func(prefix string, m map[string]any)
	var checkSlice func(prefix string, s []any)

	checkMap = func(prefix string, m map[string]any) {
		for k, v := range m {
			fullKey := k
			if prefix != "" {
				fullKey = prefix + "." + k
			}
			switch vv := v.(type) {
			case map[string]any:
				checkMap(fullKey, vv)
			case []any:
				checkSlice(fullKey, vv)
			default:
				checkStringValue(fullKey, v)
			}
		}
	}

	checkSlice = func(prefix string, s []any) {
		for i, elem := range s {
			elemKey := fmt.Sprintf("%s[%d]", prefix, i)
			switch vv := elem.(type) {
			case map[string]any:
				checkMap(elemKey, vv)
			case []any:
				checkSlice(elemKey, vv)
			default:
				checkStringValue(elemKey, elem)
			}
		}
	}

	checkMap("", sk.FrontmatterRaw)

	// Check for reserved name prefixes
	name := sk.Frontmatter.Name
	if name != "" {
		if strings.HasPrefix(name, "claude-") {
			violations = append(violations, "name starts with reserved prefix 'claude-'")
		}
		if strings.HasPrefix(name, "anthropic-") {
			violations = append(violations, "name starts with reserved prefix 'anthropic-'")
		}
	}

	if len(violations) > 0 {
		return &CheckResult{
			Name:    "spec-security",
			Passed:  false,
			Summary: fmt.Sprintf("Security risks detected: %s", strings.Join(violations, "; ")),
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "XML angle brackets and reserved prefixes pose injection and naming conflict risks"},
		}, nil
	}

	return &CheckResult{
		Name:    "spec-security",
		Passed:  true,
		Summary: "No security risks detected in frontmatter",
		Data:    &ScoreCheckData{Status: StatusOK},
	}, nil
}

// SpecVersionChecker recommends including a metadata.version field.
type SpecVersionChecker struct{}

var _ ComplianceChecker = (*SpecVersionChecker)(nil)

func (*SpecVersionChecker) Name() string { return "spec-version" }

func (*SpecVersionChecker) Check(sk skill.Skill) (*CheckResult, error) {
	if sk.FrontmatterRaw == nil {
		return &CheckResult{
			Name:    "spec-version",
			Passed:  true,
			Summary: "No metadata.version field found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include metadata.version for tracking and compatibility"},
		}, nil
	}
	meta, ok := sk.FrontmatterRaw["metadata"]
	if !ok {
		return &CheckResult{
			Name:    "spec-version",
			Passed:  true,
			Summary: "No metadata.version field found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include metadata.version for tracking and compatibility"},
		}, nil
	}
	metaMap, isMap := meta.(map[string]any)
	if !isMap {
		return &CheckResult{
			Name:    "spec-version",
			Passed:  true,
			Summary: "metadata is not a map; version field not found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include metadata.version for tracking and compatibility"},
		}, nil
	}
	ver, hasVer := metaMap["version"]
	if !hasVer {
		return &CheckResult{
			Name:    "spec-version",
			Passed:  true,
			Summary: "No metadata.version field found",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include metadata.version for tracking and compatibility"},
		}, nil
	}
	s, isStr := ver.(string)
	if isStr && strings.TrimSpace(s) == "" {
		return &CheckResult{
			Name:    "spec-version",
			Passed:  true,
			Summary: "metadata.version is empty",
			Data:    &ScoreCheckData{Status: StatusWarning, Evidence: "Best practice: include a non-empty metadata.version"},
		}, nil
	}
	return &CheckResult{
		Name:    "spec-version",
		Passed:  true,
		Summary: "metadata.version present",
		Data:    &ScoreCheckData{Status: StatusOptimal},
	}, nil
}
