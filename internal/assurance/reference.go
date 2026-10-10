package assurance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

const (
	ReferenceVersion  = "1.0"
	ReferenceKind     = "waza.grader-reference"
	ReviewKind        = "waza.grader-review"
	AgreementProtocol = "fixed_corpus_agreement_v1"
	MaxLabelBytes     = 1024 * 1024
	MaxReferenceCases = 1000
)

type CaseClassification string

const (
	CaseGood        CaseClassification = "good"
	CaseAlternative CaseClassification = "alternative_valid"
	CaseCriticalBad CaseClassification = "critical_bad"
)

// ReferenceDocument binds labels to source bytes and native declarations.
// An absent implementation digest is unavailable, never a version-string match.
type ReferenceDocument struct {
	SchemaVersion                  string           `json:"schema_version"`
	Kind                           string           `json:"kind"`
	ID                             string           `json:"id"`
	Version                        string           `json:"version"`
	EvalSourceSHA256               string           `json:"eval_source_sha256"`
	EvalResolvedConfigSHA256       string           `json:"eval_resolved_config_sha256"`
	ImplementationExecutableSHA256 *string          `json:"implementation_executable_sha256"`
	Domains                        []DomainCriteria `json:"domains"`
	Calibration                    *CalibrationPlan `json:"calibration"`
	Cases                          []ReferenceCase  `json:"cases"`
}

// MinimumCases counts distinct supplied cases, not repeats or judge calls.
// MinimumAgreement is a finite-corpus criterion, not statistical confidence.
type DomainCriteria struct {
	ID               string  `json:"id"`
	MinimumCases     int     `json:"minimum_cases"`
	MinimumAgreement float64 `json:"minimum_agreement"`
}

type CalibrationPlan struct {
	Protocol           string `json:"protocol"`
	Model              string `json:"model"`
	MaxJudgeExecutions int    `json:"max_judge_executions"`
}

type ReferenceCase struct {
	ID                    string             `json:"id"`
	ScenarioID            string             `json:"scenario_id"`
	Domain                string             `json:"domain"`
	Classification        CaseClassification `json:"classification"`
	TaskID                string             `json:"task_id"`
	TaskDeclarationSHA256 string             `json:"task_declaration_sha256"`
	Snapshot              string             `json:"snapshot,omitempty"`
	AuthoredInput         *AuthoredSource    `json:"authored_input,omitempty"`
	ManifestSHA256        string             `json:"manifest_sha256"`
	Checks                []ReferenceCheck   `json:"checks"`
}

type AuthoredSource struct {
	Path           string `json:"path"`
	DocumentSHA256 string `json:"document_sha256"`
}

type ReferenceCheck struct {
	RequirementID           string                     `json:"requirement_id"`
	Check                   models.RequirementCheck    `json:"check"`
	GraderDeclarationSHA256 string                     `json:"grader_declaration_sha256"`
	RubricContentSHA256     *string                    `json:"rubric_content_sha256"`
	ExpectedPassed          *bool                      `json:"expected_passed"`
	Evidence                []models.EvidenceReference `json:"evidence"`
}

// ReferenceSet retains exact reviewed bytes. Accessors return independent copies.
type ReferenceSet struct {
	document ReferenceDocument
	source   []byte
}

func ParseReferences(data []byte) (*ReferenceSet, error) {
	var document ReferenceDocument
	value, err := decodeDocument(data, &document)
	if err != nil {
		return nil, err
	}
	if document.SchemaVersion != ReferenceVersion || document.Kind != ReferenceKind {
		return nil, errors.New("reference labels: unsupported document kind or version")
	}
	if err := validateReferenceDocument(document, value); err != nil {
		return nil, err
	}
	return &ReferenceSet{document: document, source: bytes.Clone(data)}, nil
}

func (s *ReferenceSet) Subject() (ReviewSubject, error) {
	if s == nil || len(s.source) == 0 {
		return ReviewSubject{}, errors.New("reference labels: uninitialized reference set")
	}
	return ReviewSubject{ID: s.document.ID, Version: s.document.Version, Labels: bytes.Clone(s.source)}, nil
}

func (s *ReferenceSet) Document() (ReferenceDocument, error) {
	if s == nil || len(s.source) == 0 {
		return ReferenceDocument{}, errors.New("reference labels: uninitialized reference set")
	}
	var document ReferenceDocument
	// Re-decoding immutable, admitted bytes also isolates nested pointers/slices.
	if _, err := decodeDocument(s.source, &document); err != nil {
		return ReferenceDocument{}, err
	}
	return document, nil
}

type ReviewDocument struct {
	SchemaVersion  string      `json:"schema_version"`
	Kind           string      `json:"kind"`
	SourceID       string      `json:"source_id"`
	SubjectID      string      `json:"subject_id"`
	SubjectVersion string      `json:"subject_version"`
	LabelsSHA256   string      `json:"labels_sha256"`
	State          ReviewState `json:"state"`
	Reviewer       string      `json:"reviewer"`
	ReviewedAt     time.Time   `json:"reviewed_at"`
}

// ParseReview admits a declaration, not a trusted or authenticated human review.
// Current-source acceptance remains a separate evaluator-owned argument.
func ParseReview(data []byte) (SuppliedReview, error) {
	var document ReviewDocument
	if _, err := decodeDocument(data, &document); err != nil {
		return SuppliedReview{}, err
	}
	if document.SchemaVersion != ReferenceVersion || document.Kind != ReviewKind {
		return SuppliedReview{}, errors.New("reference review: unsupported document kind or version")
	}
	for _, identifier := range []string{document.SourceID, document.SubjectID, document.SubjectVersion} {
		if !referenceIdentifier(identifier) {
			return SuppliedReview{}, errors.New("reference review: invalid identity")
		}
	}
	if !validSHA256(document.LabelsSHA256) {
		return SuppliedReview{}, errors.New("reference review: invalid label byte digest")
	}
	switch document.State {
	case ReviewUnreviewed, ReviewSubmitted:
	case ReviewReviewed, ReviewRejected, ReviewRevoked:
		if !referenceIdentifier(document.Reviewer) || document.ReviewedAt.IsZero() {
			return SuppliedReview{}, errors.New("reference review: a decision requires reviewer and decision time")
		}
	default:
		return SuppliedReview{}, errors.New("reference review: unsupported review state")
	}
	digest, err := hex.DecodeString(document.LabelsSHA256)
	if err != nil {
		return SuppliedReview{}, errors.New("reference review: invalid label byte digest")
	}
	return SuppliedReview{
		SourceID: document.SourceID,
		Decision: &ReviewDecision{
			SubjectID: document.SubjectID, SubjectVersion: document.SubjectVersion,
			LabelsDigest: [sha256.Size]byte(digest), State: document.State,
			Reviewer: document.Reviewer, ReviewedAt: document.ReviewedAt,
		},
	}, nil
}

func decodeDocument(data []byte, destination any) (any, error) {
	if len(data) == 0 || len(data) > MaxLabelBytes {
		return nil, errors.New("reference document: empty or oversized input")
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, errors.New("reference document: ambiguous or malformed JSON")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("reference document: expected an object")
	}
	if !exactDocumentFields(value, reflect.TypeOf(destination)) {
		return nil, errors.New("reference document: unsupported property names")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return nil, errors.New("reference document: unsupported fields or value types")
	}
	return value, nil
}

func exactDocumentFields(value any, typ reflect.Type) bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch object := value.(type) {
	case map[string]any:
		if typ.Kind() != reflect.Struct {
			return false
		}
		fields := make(map[string]reflect.Type)
		for field := range typ.Fields() {
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.IsExported() && name != "-" {
				if name == "" {
					name = field.Name
				}
				fields[name] = field.Type
			}
		}
		for name, child := range object {
			field, exists := fields[name]
			if !exists || !exactDocumentFields(child, field) {
				return false
			}
		}
	case []any:
		if typ.Kind() != reflect.Slice && typ.Kind() != reflect.Array {
			return false
		}
		for _, child := range object {
			if !exactDocumentFields(child, typ.Elem()) {
				return false
			}
		}
	}
	return true
}

func validateReferenceDocument(document ReferenceDocument, value any) error {
	if !referenceIdentifier(document.ID) || !referenceIdentifier(document.Version) ||
		!validSHA256(document.EvalSourceSHA256) || !validSHA256(document.EvalResolvedConfigSHA256) ||
		(document.ImplementationExecutableSHA256 != nil && !validSHA256(*document.ImplementationExecutableSHA256)) {
		return errors.New("reference labels: invalid subject or digest binding")
	}
	if len(document.Domains) == 0 || len(document.Domains) > 100 ||
		len(document.Cases) == 0 || len(document.Cases) > MaxReferenceCases {
		return errors.New("reference labels: invalid domain or case count")
	}
	domains := make(map[string]bool, len(document.Domains))
	for _, domain := range document.Domains {
		if !referenceIdentifier(domain.ID) || domains[domain.ID] ||
			domain.MinimumCases < 1 || domain.MinimumCases > MaxReferenceCases ||
			math.IsNaN(domain.MinimumAgreement) || math.IsInf(domain.MinimumAgreement, 0) ||
			domain.MinimumAgreement <= 0 || domain.MinimumAgreement > 1 {
			return errors.New("reference labels: invalid or duplicate domain criteria")
		}
		domains[domain.ID] = true
	}
	if plan := document.Calibration; plan != nil {
		if plan.Protocol != AgreementProtocol || !referenceIdentifier(plan.Model) ||
			plan.MaxJudgeExecutions < 1 || plan.MaxJudgeExecutions > MaxReferenceCases*100 {
			return errors.New("reference labels: unsupported calibration plan")
		}
	}
	cases := make(map[string]bool, len(document.Cases))
	manifests := make(map[string]bool, len(document.Cases))
	rawDocument, ok := value.(map[string]any)
	if !ok {
		return errors.New("reference labels: expected an object")
	}
	rawCases, ok := rawDocument["cases"].([]any)
	if !ok {
		return errors.New("reference labels: expected case objects")
	}
	for i, candidate := range document.Cases {
		if !referenceIdentifier(candidate.ID) || cases[candidate.ID] ||
			!referenceIdentifier(candidate.ScenarioID) || !domains[candidate.Domain] ||
			!referenceIdentifier(candidate.TaskID) || !validSHA256(candidate.TaskDeclarationSHA256) ||
			!validSHA256(candidate.ManifestSHA256) || manifests[candidate.ManifestSHA256] ||
			len(candidate.Checks) == 0 || len(candidate.Checks) > 100 {
			return errors.New("reference labels: invalid or repeated case binding")
		}
		cases[candidate.ID], manifests[candidate.ManifestSHA256] = true, true
		switch candidate.Classification {
		case CaseGood, CaseAlternative, CaseCriticalBad:
		default:
			return errors.New("reference labels: unsupported case classification")
		}
		rawCase, ok := rawCases[i].(map[string]any)
		if !ok {
			return errors.New("reference labels: expected a case object")
		}
		_, hasSnapshot := rawCase["snapshot"]
		_, hasAuthored := rawCase["authored_input"]
		if hasSnapshot == hasAuthored ||
			(hasSnapshot && !safeReferencePath(candidate.Snapshot)) ||
			(hasAuthored && (candidate.AuthoredInput == nil ||
				!safeReferencePath(candidate.AuthoredInput.Path) || !validSHA256(candidate.AuthoredInput.DocumentSHA256))) {
			return errors.New("reference labels: exactly one valid historical or authored source is required")
		}
		rawChecks, ok := rawCase["checks"].([]any)
		if !ok {
			return errors.New("reference labels: expected check objects")
		}
		checks := make(map[struct {
			requirement string
			check       models.RequirementCheck
		}]bool)
		hasRejection := false
		var origin *models.EvidenceOrigin
		for j, check := range candidate.Checks {
			identity := struct {
				requirement string
				check       models.RequirementCheck
			}{check.RequirementID, check.Check}
			if !referenceIdentifier(check.RequirementID) || !referenceIdentifier(check.Check.Grader) ||
				checks[identity] || !validSHA256(check.GraderDeclarationSHA256) ||
				(check.RubricContentSHA256 != nil && !validSHA256(*check.RubricContentSHA256)) ||
				check.ExpectedPassed == nil || len(check.Evidence) == 0 || len(check.Evidence) > 256 {
				return errors.New("reference labels: invalid or duplicate check")
			}
			checks[identity] = true
			rawCheckItem, ok := rawChecks[j].(map[string]any)
			if !ok {
				return errors.New("reference labels: expected a check object")
			}
			rawCheck, ok := rawCheckItem["check"].(map[string]any)
			if !ok {
				return errors.New("reference labels: expected a scoped check object")
			}
			_, hasAfterTurn := rawCheck["after_turn"]
			switch check.Check.Scope {
			case "eval", "task":
				if hasAfterTurn {
					return errors.New("reference labels: after_turn must be absent outside checkpoint scope")
				}
			case "checkpoint":
				if !hasAfterTurn || check.Check.AfterTurn < 1 {
					return errors.New("reference labels: checkpoint requires a positive after_turn")
				}
			default:
				return errors.New("reference labels: unsupported check scope")
			}
			if !*check.ExpectedPassed {
				hasRejection = true
				if candidate.Classification != CaseCriticalBad {
					return errors.New("reference labels: valid cases cannot expect a rejected check")
				}
			}
			references := make(map[models.EvidenceReference]bool, len(check.Evidence))
			for _, reference := range check.Evidence {
				if !evidence.CompleteOrigin(reference.Origin) || !referenceIdentifier(reference.Origin.EvalID) ||
					reference.Origin.TaskID != candidate.TaskID ||
					!referenceIdentifier(reference.ArtifactID) || references[reference] || !relativePointer(reference.Pointer) {
					return errors.New("reference labels: invalid or duplicate evidence reference")
				}
				if origin == nil {
					origin = new(reference.Origin)
				} else if *origin != reference.Origin {
					return errors.New("reference labels: case references require one exact origin")
				}
				references[reference] = true
			}
		}
		if candidate.Classification == CaseCriticalBad && !hasRejection {
			return errors.New("reference labels: critical bad case requires an intended rejection")
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func referenceIdentifier(value string) bool {
	if value == "" || len(value) > 256 || value != strings.TrimSpace(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func safeReferencePath(value string) bool {
	if value == "" || len(value) > 1024 || path.IsAbs(value) ||
		strings.ContainsAny(value, "\\:") || !referenceIdentifier(value) || path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "." || component == ".." || component == "" {
			return false
		}
	}
	return true
}

func relativePointer(value string) bool {
	if value == "" {
		return true
	}
	if !strings.HasPrefix(value, "/") || len(value) > 1024 ||
		strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] == '~' {
			i++
			if i == len(value) || (value[i] != '0' && value[i] != '1') {
				return false
			}
		}
	}
	return true
}
