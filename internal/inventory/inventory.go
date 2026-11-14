// Package inventory generates and offline-verifies runner image and plugin
// component inventories. An inventory is a closed CycloneDX 1.6 JSON profile
// (waza.component-inventory/1.0) whose metadata.component is the inventoried
// subject bound by its sha256 digest. Verification binds the inventory to the
// digest the verifier actually consumed and fails closed on any inventory that
// is missing, malformed, unbound, mismatched or altered. Inventories are
// evidence for operator policy; they do not make vulnerability decisions.
package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Profile     = "waza.component-inventory/1.0"
	MediaType   = "application/vnd.cyclonedx+json"
	BindingKind = "component-inventory"

	ReportKind    = "waza.inventory-verification"
	ReportVersion = "1.0"

	SubjectImage  = "image"
	SubjectPlugin = "plugin"

	propertyProfile     = "waza:inventory.profile"
	propertySubjectKind = "waza:subject.kind"

	// MaxInventoryBytes matches the provenance binding member bound.
	MaxInventoryBytes = 16 << 20
	maxComponents     = 20000
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// Document is the closed CycloneDX subset emitted and accepted by the profile.
type Document struct {
	BOMFormat   string      `json:"bomFormat"`
	SpecVersion string      `json:"specVersion"`
	Version     int         `json:"version"`
	Metadata    Metadata    `json:"metadata"`
	Components  []Component `json:"components"`
}

type Metadata struct {
	Component  Component  `json:"component"`
	Properties []Property `json:"properties"`
}

type Component struct {
	BOMRef  string `json:"bom-ref"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	PURL    string `json:"purl,omitempty"`
	Hashes  []Hash `json:"hashes,omitempty"`
}

type Hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type Property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Subject is the inventoried image or plugin.
type Subject struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
	PURL   string `json:"purl,omitempty"`
}

var schemaFiles = map[string]struct{ file, text string }{
	"inventory":    {"component-inventory-1.0.schema.json", schemas.ComponentInventorySchemaJSON},
	"verification": {"inventory-verification-1.0.schema.json", schemas.InventoryVerificationSchemaJSON},
}

var compiledSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	base := "https://microsoft.github.io/waza/schemas/"
	for _, s := range schemaFiles {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(s.text))
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(base+s.file, doc); err != nil {
			return nil, err
		}
	}
	out := map[string]*jsonschema.Schema{}
	for name, s := range schemaFiles {
		compiled, err := c.Compile(base + s.file)
		if err != nil {
			return nil, err
		}
		out[name] = compiled
	}
	return out, nil
})

// decodeStrict rejects duplicate keys and schema violations before decoding.
func decodeStrict(data []byte, schema string, target any) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	compiled, err := compiledSchemas()
	if err != nil {
		return fmt.Errorf("compiling inventory schemas: %w", err)
	}
	if err := compiled[schema].Validate(doc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return json.Unmarshal(data, target)
}

// Parse decodes an inventory in the profile without binding it to a digest.
func Parse(data []byte) (*Document, error) {
	if len(data) > MaxInventoryBytes {
		return nil, fmt.Errorf("inventory exceeds %d bytes", MaxInventoryBytes)
	}
	var doc Document
	if err := decodeStrict(data, "inventory", &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ValidateReport checks a verification report against its published schema.
func ValidateReport(data []byte) error {
	var r Report
	return decodeStrict(data, "verification", &r)
}

func (d *Document) property(name string) string {
	for _, p := range d.Metadata.Properties {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// Subject returns the bound subject. The digest is empty when unbound.
func (d *Document) Subject() Subject {
	c := d.Metadata.Component
	s := Subject{Kind: d.property(propertySubjectKind), Name: c.Name, PURL: c.PURL}
	if len(c.Hashes) == 1 && c.Hashes[0].Alg == "SHA-256" {
		s.Digest = "sha256:" + c.Hashes[0].Content
	}
	return s
}

// ImageRepository strips a tag or digest from an image reference.
func ImageRepository(reference string) string {
	repo, _, _ := strings.Cut(reference, "@")
	if colon := strings.LastIndex(repo, ":"); colon > strings.LastIndex(repo, "/") {
		repo = repo[:colon]
	}
	return repo
}

// imagePURL is the package URL for an OCI image digest.
func imagePURL(repository, digest string) string {
	name := repository[strings.LastIndex(repository, "/")+1:]
	return "pkg:oci/" + strings.ToLower(name) + "@" + strings.Replace(digest, ":", "%3A", 1) +
		"?repository_url=" + strings.ToLower(repository)
}

// DigestOf returns the sha256 digest of bytes as sha256:<hex>.
func DigestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
