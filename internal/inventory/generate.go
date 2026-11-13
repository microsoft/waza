package inventory

import (
	"bufio"
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// GenerateInput describes one inventory. SubjectDigest must be the immutable
// sha256 digest of the image manifest or plugin artifact being described.
type GenerateInput struct {
	SubjectKind   string
	SubjectName   string
	SubjectDigest string
	Components    []Component
}

// Generate renders a canonical inventory and proves it verifies against its
// own subject before returning it.
func Generate(in GenerateInput) ([]byte, error) {
	if in.SubjectKind != SubjectImage && in.SubjectKind != SubjectPlugin {
		return nil, fmt.Errorf("subject kind %q must be image or plugin", in.SubjectKind)
	}
	if !digestPattern.MatchString(in.SubjectDigest) {
		return nil, fmt.Errorf("subject digest %q must be sha256:<64 hex>", in.SubjectDigest)
	}
	if in.SubjectName == "" {
		return nil, errors.New("subject name is required")
	}
	subject := Component{
		Type:   "application",
		Name:   in.SubjectName,
		Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(in.SubjectDigest, "sha256:")}},
	}
	if in.SubjectKind == SubjectImage {
		subject.Type = "container"
		subject.Name = ImageRepository(in.SubjectName)
		subject.PURL = imagePURL(subject.Name, in.SubjectDigest)
	}
	subject.BOMRef = subjectRef(Subject{Kind: in.SubjectKind, Digest: in.SubjectDigest})
	byRef := map[string]Component{}
	for _, c := range in.Components {
		c.BOMRef = componentRef(c)
		if _, dup := byRef[c.BOMRef]; !dup {
			byRef[c.BOMRef] = c
		}
	}
	if len(byRef) == 0 {
		return nil, errors.New("inventory requires at least one component")
	}
	if len(byRef) > maxComponents {
		return nil, fmt.Errorf("inventory exceeds %d components", maxComponents)
	}
	components := make([]Component, 0, len(byRef))
	for _, c := range byRef {
		components = append(components, c)
	}
	slices.SortFunc(components, func(a, b Component) int { return strings.Compare(a.BOMRef, b.BOMRef) })
	doc := Document{
		BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1,
		Metadata: Metadata{Component: subject, Properties: []Property{
			{Name: propertyProfile, Value: Profile},
			{Name: propertySubjectKind, Value: in.SubjectKind},
		}},
		Components: components,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	expect := Expectation{Kind: in.SubjectKind, Digest: in.SubjectDigest}
	if report := Verify(data, expect); report.Status != "verified" {
		failure, _ := report.FirstFailure()
		return nil, fmt.Errorf("generated inventory does not verify (%s): %s", failure.Code, failure.Message)
	}
	return data, nil
}

// GoBinaryComponents inventories a Go binary: the file itself by sha256, its
// main module, every linked dependency module and the Go standard library.
func GoBinaryComponents(path string) ([]Component, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := buildinfo.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s has no Go build information: %w", path, err)
	}
	out := []Component{{
		Type: "file", Name: filepath.Base(path),
		Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(DigestOf(data), "sha256:")}},
	}}
	if goVersion := strings.TrimPrefix(info.GoVersion, "go"); goVersion != "" {
		out = append(out, Component{Type: "library", Name: "stdlib", Version: goVersion, PURL: "pkg:golang/stdlib@" + goVersion})
	}
	if info.Main.Path != "" {
		main := Component{Type: "application", Name: info.Main.Path, PURL: "pkg:golang/" + info.Main.Path}
		if v := info.Main.Version; v != "" && v != "(devel)" {
			main.Version, main.PURL = v, main.PURL+"@"+v
		}
		out = append(out, main)
	}
	for _, dep := range info.Deps {
		if dep.Replace != nil {
			dep = dep.Replace
		}
		c := Component{Type: "library", Name: dep.Path, Version: dep.Version, PURL: "pkg:golang/" + dep.Path}
		if dep.Version != "" {
			c.PURL += "@" + dep.Version
		}
		out = append(out, c)
	}
	return out, nil
}

var debianVersionID = regexp.MustCompile(`^[0-9]+$`)

// OSRelease reads the distribution identity from an os-release file.
func OSRelease(data []byte) (id, versionID string) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			id = value
		case "VERSION_ID":
			versionID = value
		}
	}
	return id, versionID
}

// OSComponents returns the operating-system component for an os-release file.
func OSComponents(data []byte) ([]Component, error) {
	id, versionID := OSRelease(data)
	if id == "" {
		return nil, errors.New("os-release has no ID")
	}
	return []Component{{Type: "operating-system", Name: id, Version: versionID}}, nil
}

func purlVersion(v string) string {
	return strings.NewReplacer("%", "%25", ":", "%3A").Replace(v)
}

// DpkgComponents inventories installed packages from a dpkg status database.
// distro qualifies package URLs (for example debian-12) when known.
func DpkgComponents(data []byte, distroID, distroVersion string) ([]Component, error) {
	namespace := distroID
	if namespace == "" {
		namespace = "debian"
	}
	qualifier := ""
	if distroID != "" && debianVersionID.MatchString(distroVersion) {
		qualifier = "&distro=" + distroID + "-" + distroVersion
	}
	var out []Component
	for _, paragraph := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(paragraph, "\n") {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			if key, value, ok := strings.Cut(line, ":"); ok {
				fields[key] = strings.TrimSpace(value)
			}
		}
		if fields["Package"] == "" || !strings.HasSuffix(fields["Status"], " installed") {
			continue
		}
		name, version, arch := fields["Package"], fields["Version"], fields["Architecture"]
		if version == "" || arch == "" {
			return nil, fmt.Errorf("installed package %s is missing its version or architecture", name)
		}
		out = append(out, Component{
			Type: "library", Name: name, Version: version,
			PURL: "pkg:deb/" + namespace + "/" + name + "@" + purlVersion(version) + "?arch=" + arch + qualifier,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("dpkg status lists no installed packages")
	}
	return out, nil
}

// FileComponent inventories an arbitrary file by sha256 (for example a plugin
// payload member).
func FileComponent(path string) (Component, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Component{}, err
	}
	return Component{Type: "file", Name: filepath.Base(path),
		Hashes: []Hash{{Alg: "SHA-256", Content: strings.TrimPrefix(DigestOf(data), "sha256:")}}}, nil
}

// CycloneDXComponents imports top-level components from another CycloneDX
// JSON document (for example one produced by a scanner). Only fields in the
// profile are kept; nested components and non-SHA-256 hashes are dropped.
func CycloneDXComponents(data []byte) ([]Component, error) {
	var doc struct {
		BOMFormat  string `json:"bomFormat"`
		Components []struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			Version string `json:"version"`
			PURL    string `json:"purl"`
			Hashes  []Hash `json:"hashes"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.BOMFormat != "CycloneDX" {
		return nil, errors.New("document is not CycloneDX JSON")
	}
	allowed := map[string]bool{"application": true, "container": true, "file": true, "framework": true, "library": true, "operating-system": true}
	sha256Content := regexp.MustCompile(`^[a-f0-9]{64}$`)
	var out []Component
	for _, c := range doc.Components {
		if c.Name == "" || len(c.Name) > 512 || len(c.Version) > 256 || len(c.PURL) > 2048 {
			continue
		}
		if !allowed[c.Type] {
			c.Type = "library"
		}
		if c.PURL != "" && !strings.HasPrefix(c.PURL, "pkg:") {
			c.PURL = ""
		}
		component := Component{Type: c.Type, Name: c.Name, Version: c.Version, PURL: c.PURL}
		for _, h := range c.Hashes {
			if h.Alg == "SHA-256" && sha256Content.MatchString(strings.ToLower(h.Content)) {
				component.Hashes = []Hash{{Alg: "SHA-256", Content: strings.ToLower(h.Content)}}
				break
			}
		}
		out = append(out, component)
	}
	return out, nil
}
