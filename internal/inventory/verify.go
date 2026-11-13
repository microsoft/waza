package inventory

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Check identifiers in report order.
var checkIDs = []string{"inventory", "profile", "subject", "binding", "components"}

// Expectation is what the verifier consumed. Digest is required: an image or
// plugin whose digest is not known cannot be bound and fails verification.
type Expectation struct {
	Kind   string `json:"kind"`
	Digest string `json:"digest"`
	// Name optionally pins the subject name (image repository or plugin name).
	Name string `json:"name,omitempty"`
}

type Report struct {
	Kind            string      `json:"kind"`
	SchemaVersion   string      `json:"schema_version"`
	Status          string      `json:"status"`
	ExitCode        int         `json:"exit_code"`
	InventorySHA256 string      `json:"inventory_sha256,omitempty"`
	Expected        Expectation `json:"expected"`
	Subject         *Subject    `json:"subject,omitempty"`
	Summary         *Summary    `json:"summary,omitempty"`
	Checks          []Check     `json:"checks"`
}

type Summary struct {
	Total       int            `json:"total"`
	ByType      map[string]int `json:"by_type"`
	ByEcosystem map[string]int `json:"by_ecosystem"`
}

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Failures returns failed checks.
func (r *Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Status == "failed" {
			out = append(out, c)
		}
	}
	return out
}

// FirstFailure returns the first failed check, if any.
func (r *Report) FirstFailure() (Check, bool) {
	failures := r.Failures()
	if len(failures) == 0 {
		return Check{}, false
	}
	return failures[0], true
}

type verification struct {
	report *Report
	failed bool
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit-3]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "..."
}

func (v *verification) set(id, status, code, format string, args ...any) {
	message := clip(fmt.Sprintf(format, args...), 1024)
	for i := range v.report.Checks {
		if v.report.Checks[i].ID == id {
			v.report.Checks[i] = Check{ID: id, Status: status, Code: code, Message: message}
		}
	}
	if status == "failed" {
		v.failed = true
	}
}

func (v *verification) fail(id, code, format string, args ...any) bool {
	v.set(id, "failed", code, format, args...)
	return false
}

// Verify binds an inventory to the expected subject digest offline. Data may be
// nil to represent a missing inventory, which fails explicitly.
func Verify(data []byte, expect Expectation) *Report {
	r := &Report{Kind: ReportKind, SchemaVersion: ReportVersion, Expected: expect}
	if r.Expected.Kind != SubjectPlugin {
		r.Expected.Kind = SubjectImage
	}
	// Over-long values cannot match and are clipped to keep the report in bounds.
	r.Expected.Digest = clip(r.Expected.Digest, 128)
	r.Expected.Name = clip(r.Expected.Name, 512)
	for _, id := range checkIDs {
		r.Checks = append(r.Checks, Check{ID: id, Status: "skipped", Code: "not_reached", Message: "an earlier check failed"})
	}
	v := &verification{report: r}
	v.run(data)
	r.Status, r.ExitCode = "verified", 0
	if v.failed {
		r.Status, r.ExitCode = "failed", 1
	}
	return r
}

func (v *verification) run(data []byte) {
	r := v.report
	switch {
	case data == nil:
		v.fail("inventory", "inventory_missing", "no component inventory was supplied; the %s cannot be verified", r.Expected.Kind)
		return
	case len(data) > MaxInventoryBytes:
		v.fail("inventory", "inventory_too_large", "inventory exceeds %d bytes", MaxInventoryBytes)
		return
	}
	r.InventorySHA256 = strings.TrimPrefix(DigestOf(data), "sha256:")
	doc, err := Parse(data)
	if err != nil {
		v.fail("inventory", "inventory_malformed", "inventory is not a %s document: %v", Profile, err)
		return
	}
	v.set("inventory", "passed", "ok", "CycloneDX %s inventory, %d bytes", doc.SpecVersion, len(data))
	if !v.profile(doc) || !v.subject(doc) || !v.binding(doc) {
		return
	}
	v.components(doc)
}

func (v *verification) profile(doc *Document) bool {
	if got := doc.property(propertyProfile); got != Profile {
		return v.fail("profile", "profile_unsupported", "inventory profile %q is not %s", got, Profile)
	}
	v.set("profile", "passed", "ok", "%s", Profile)
	return true
}

func (v *verification) subject(doc *Document) bool {
	s := doc.Subject()
	v.report.Subject = &s
	wantType := map[string]string{SubjectImage: "container", SubjectPlugin: "application"}[s.Kind]
	switch {
	case wantType == "":
		return v.fail("subject", "subject_kind_invalid", "subject kind %q is not image or plugin", s.Kind)
	case doc.Metadata.Component.Type != wantType:
		return v.fail("subject", "subject_kind_invalid", "%s subject must have component type %s, got %s", s.Kind, wantType, doc.Metadata.Component.Type)
	case !digestPattern.MatchString(s.Digest):
		return v.fail("subject", "subject_unbound", "inventory does not bind its subject to a sha256 digest")
	case s.Kind == SubjectImage && s.PURL != imagePURL(s.Name, s.Digest):
		return v.fail("subject", "subject_purl_mismatch", "image purl %q does not name %s@%s", s.PURL, s.Name, s.Digest)
	case doc.Metadata.Component.BOMRef != subjectRef(s):
		return v.fail("subject", "subject_ref_mismatch", "subject bom-ref %q does not name its bound digest", doc.Metadata.Component.BOMRef)
	}
	v.set("subject", "passed", "ok", "%s %s bound to %s", s.Kind, s.Name, s.Digest)
	return true
}

func (v *verification) binding(doc *Document) bool {
	s, want := doc.Subject(), v.report.Expected
	switch {
	case want.Digest == "":
		return v.fail("binding", "expected_digest_missing", "no consumed %s digest was supplied; an inventory cannot be bound to an unidentified %s", want.Kind, want.Kind)
	case !digestPattern.MatchString(want.Digest):
		return v.fail("binding", "expected_digest_invalid", "consumed %s digest %q is not sha256:<64 hex>; tags and mutable references are not accepted", want.Kind, want.Digest)
	case s.Kind != want.Kind:
		return v.fail("binding", "subject_kind_mismatch", "inventory describes a %s, but a %s was consumed", s.Kind, want.Kind)
	case s.Digest != want.Digest:
		return v.fail("binding", "subject_digest_mismatch", "inventory is bound to %s, but the consumed %s is %s", s.Digest, want.Kind, want.Digest)
	case want.Name != "" && s.Name != want.Name:
		return v.fail("binding", "subject_name_mismatch", "inventory names %s, but the consumed %s is %s", s.Name, want.Kind, want.Name)
	}
	v.set("binding", "passed", "ok", "inventory is bound to the consumed %s %s", want.Kind, want.Digest)
	return true
}

func (v *verification) components(doc *Document) {
	if len(doc.Components) == 0 {
		v.fail("components", "components_empty", "inventory lists no components")
		return
	}
	summary := &Summary{ByType: map[string]int{}, ByEcosystem: map[string]int{}}
	seen := map[string]bool{}
	for i, c := range doc.Components {
		if seen[c.BOMRef] {
			v.fail("components", "component_duplicate", "component %d repeats bom-ref %q", i, c.BOMRef)
			return
		}
		seen[c.BOMRef] = true
		if c.BOMRef != componentRef(c) {
			v.fail("components", "component_ref_mismatch", "component %d bom-ref %q does not match its identity", i, c.BOMRef)
			return
		}
		summary.Total++
		summary.ByType[c.Type]++
		summary.ByEcosystem[ecosystem(c.PURL)]++
	}
	v.report.Summary = summary
	v.set("components", "passed", "ok", "%d components", summary.Total)
}

func ecosystem(purl string) string {
	if rest, ok := strings.CutPrefix(purl, "pkg:"); ok {
		if typ, _, ok := strings.Cut(rest, "/"); ok && typ != "" {
			return typ
		}
	}
	return "none"
}

func subjectRef(s Subject) string {
	return s.Kind + ":" + s.Digest
}

// componentRef is the canonical bom-ref: the purl, or type/name@version with
// an optional sha256 for components without a package URL.
func componentRef(c Component) string {
	if c.PURL != "" {
		return c.PURL
	}
	ref := c.Type + "/" + c.Name
	if c.Version != "" {
		ref += "@" + c.Version
	}
	if len(c.Hashes) == 1 {
		ref += "#sha256:" + c.Hashes[0].Content
	}
	return ref
}

// EncodeReport renders a schema-valid report.
func EncodeReport(r *Report) ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := ValidateReport(data); err != nil {
		return nil, fmt.Errorf("inventory report violates its schema: %w", err)
	}
	return append(data, '\n'), nil
}
