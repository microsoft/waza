package registry

import (
	"fmt"
	"path"
	"strings"
)

// Ref is a Go-module-style Waza registry reference.
type Ref struct {
	Raw     string
	Host    string
	Owner   string
	Repo    string
	Path    string
	Export  string
	Version string
}

func ParseRef(raw string) (Ref, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Ref{}, fmt.Errorf("ref is empty")
	}
	if strings.Contains(raw, `\`) {
		return Ref{}, fmt.Errorf("ref %q must use forward-slash paths", raw)
	}
	base, version, ok := strings.Cut(raw, "@")
	if !ok || strings.TrimSpace(version) == "" {
		return Ref{}, fmt.Errorf("ref %q must include @<version>", raw)
	}
	base, export, _ := strings.Cut(base, "#")
	parts := strings.Split(base, "/")
	if len(parts) < 3 {
		return Ref{}, fmt.Errorf("ref %q must use <host>/<owner>/<repo>[/path]@<version>", raw)
	}
	for i, part := range parts[:3] {
		if err := validateModulePathSegment(part); err != nil {
			return Ref{}, fmt.Errorf("ref %q has invalid module segment %d: %w", raw, i, err)
		}
	}
	refPath := ""
	if len(parts) > 3 {
		rawPath := strings.Join(parts[3:], "/")
		if hasParentPathSegment(rawPath) {
			return Ref{}, fmt.Errorf("ref %q has invalid module path %q", raw, rawPath)
		}
		refPath = path.Clean(strings.Join(parts[3:], "/"))
		if refPath == "." {
			refPath = ""
		}
		if strings.HasPrefix(refPath, "../") || refPath == ".." || strings.HasPrefix(refPath, "/") {
			return Ref{}, fmt.Errorf("ref %q has invalid module path %q", raw, refPath)
		}
	}
	return Ref{
		Raw:     raw,
		Host:    parts[0],
		Owner:   parts[1],
		Repo:    parts[2],
		Path:    refPath,
		Export:  strings.TrimSpace(export),
		Version: strings.TrimSpace(version),
	}, nil
}

func (r Ref) ModulePath() string {
	return r.Host + "/" + r.Owner + "/" + r.Repo
}

func validateModulePathSegment(segment string) error {
	if segment == "" {
		return fmt.Errorf("empty segment")
	}
	if strings.TrimSpace(segment) != segment {
		return fmt.Errorf("contains surrounding whitespace")
	}
	if segment == "." || segment == ".." {
		return fmt.Errorf("must not be %q", segment)
	}
	if strings.ContainsAny(segment, `/\:`) {
		return fmt.Errorf("contains a path separator or drive separator")
	}
	return nil
}

func hasParentPathSegment(slashPath string) bool {
	for _, segment := range strings.Split(slashPath, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}
