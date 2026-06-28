package graders

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"
)

//go:embed data/rubrics/*.md
var builtinRubricsFS embed.FS

const builtinRubricsDir = "data/rubrics"

// RubricScale identifies the scoring scale of a rubric. Only pass-fail is
// currently honored by the prompt grader's tool-call scoring; "1-5" is also
// accepted in the schema so a future graded rubric reader does not require a
// breaking change.
type RubricScale string

const (
	RubricScalePassFail RubricScale = "pass-fail"
	RubricScaleOneFive  RubricScale = "1-5"
)

// RubricExpected is the expected outcome for a golden example.
type RubricExpected string

const (
	RubricExpectedPass RubricExpected = "pass"
	RubricExpectedFail RubricExpected = "fail"
)

// RubricGolden is a worked example bundled with a rubric. Each shipped rubric
// must include enough goldens for an oracle judge test to exercise both the
// pass and fail paths of the rubric.
type RubricGolden struct {
	Name     string         `yaml:"name"`
	Input    string         `yaml:"input,omitempty"`
	Output   string         `yaml:"output"`
	Context  string         `yaml:"context,omitempty"`
	Expected RubricExpected `yaml:"expected"`
}

// RubricFrontmatter is the YAML header of a rubric file.
type RubricFrontmatter struct {
	Name        string         `yaml:"name"`
	Version     string         `yaml:"version"`
	Scale       RubricScale    `yaml:"scale"`
	Description string         `yaml:"description"`
	Goldens     []RubricGolden `yaml:"goldens,omitempty"`
}

// Rubric is a parsed rubric file: frontmatter + markdown body.
type Rubric struct {
	RubricFrontmatter
	// Body is the markdown prompt the judge LLM receives.
	Body string
	// Source records where the rubric came from for diagnostics
	// ("builtin:groundedness" or "file:/path/to/rubric.md").
	Source string
}

// ParseRubric parses a rubric document of the form:
//
//	---
//	<yaml frontmatter>
//	---
//	<markdown body>
func ParseRubric(data []byte) (*Rubric, error) {
	body, fm, err := splitRubricFrontmatter(data)
	if err != nil {
		return nil, err
	}

	var meta RubricFrontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return nil, fmt.Errorf("invalid rubric frontmatter: %w", err)
	}

	r := &Rubric{RubricFrontmatter: meta, Body: strings.TrimSpace(string(body))}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

func splitRubricFrontmatter(data []byte) (body, frontmatter []byte, err error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return nil, nil, errors.New("rubric must start with --- frontmatter")
	}
	rest := bytes.TrimPrefix(trimmed, []byte("---"))
	rest = bytes.TrimLeft(rest, "\r\n")
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, nil, errors.New("rubric frontmatter missing closing ---")
	}
	fm := rest[:end]
	body = rest[end+len("\n---"):]
	body = bytes.TrimLeft(body, "\r\n")
	return body, fm, nil
}

// Validate checks that required fields are present and well-formed.
func (r *Rubric) Validate() error {
	if r == nil {
		return errors.New("nil rubric")
	}
	if strings.TrimSpace(r.Name) == "" {
		return errors.New("rubric.name is required")
	}
	if strings.TrimSpace(r.Version) == "" {
		return fmt.Errorf("rubric %q: version is required", r.Name)
	}
	if _, err := semver.NewVersion(r.Version); err != nil {
		return fmt.Errorf("rubric %q: version %q is not valid semver: %w", r.Name, r.Version, err)
	}
	switch r.Scale {
	case RubricScalePassFail, RubricScaleOneFive:
	case "":
		return fmt.Errorf("rubric %q: scale is required", r.Name)
	default:
		return fmt.Errorf("rubric %q: scale %q is not supported (use %q or %q)", r.Name, r.Scale, RubricScalePassFail, RubricScaleOneFive)
	}
	if strings.TrimSpace(r.Description) == "" {
		return fmt.Errorf("rubric %q: description is required", r.Name)
	}
	if strings.TrimSpace(r.Body) == "" {
		return fmt.Errorf("rubric %q: body is required", r.Name)
	}
	for i, g := range r.Goldens {
		if strings.TrimSpace(g.Name) == "" {
			return fmt.Errorf("rubric %q: goldens[%d].name is required", r.Name, i)
		}
		if strings.TrimSpace(g.Output) == "" {
			return fmt.Errorf("rubric %q: goldens[%d].output is required", r.Name, i)
		}
		if g.Expected != RubricExpectedPass && g.Expected != RubricExpectedFail {
			return fmt.Errorf("rubric %q: goldens[%d].expected must be %q or %q", r.Name, i, RubricExpectedPass, RubricExpectedFail)
		}
	}
	return nil
}

// ResolveRubric resolves a rubric reference. A reference is treated as a path
// when it contains a path separator, starts with "." or "/" or "~", or has a
// ".md" suffix — otherwise it is looked up in the built-in rubric set by name.
func ResolveRubric(ref string) (*Rubric, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, errors.New("rubric reference is empty")
	}
	if isRubricPathRef(ref) {
		return LoadRubricFile(ref)
	}
	return LoadBuiltinRubric(ref)
}

func isRubricPathRef(ref string) bool {
	if strings.HasSuffix(ref, ".md") {
		return true
	}
	if strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") {
		return true
	}
	if strings.ContainsRune(ref, '/') || strings.ContainsRune(ref, os.PathSeparator) {
		return true
	}
	return false
}

// LoadBuiltinRubric loads a shipped rubric by name (e.g. "groundedness").
func LoadBuiltinRubric(name string) (*Rubric, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("rubric name is empty")
	}
	path := builtinRubricsDir + "/" + name + ".md"
	data, err := builtinRubricsFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("unknown built-in rubric %q. Available: %s", name, strings.Join(BuiltinRubricNames(), ", "))
		}
		return nil, fmt.Errorf("read built-in rubric %q: %w", name, err)
	}
	r, err := ParseRubric(data)
	if err != nil {
		return nil, fmt.Errorf("parse built-in rubric %q: %w", name, err)
	}
	r.Source = "builtin:" + name
	return r, nil
}

// LoadRubricFile loads a rubric from a markdown file on disk. Leading "~/"
// (or a bare "~") is expanded to the current user's home directory. A bare
// "~name" prefix is intentionally NOT expanded (shells treat that as another
// user's home, which Go has no portable resolver for); such a path is read
// literally and will surface a normal file-not-found error if that's wrong.
func LoadRubricFile(path string) (*Rubric, error) {
	resolved := path
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				resolved = home
			} else {
				resolved = filepath.Join(home, strings.TrimPrefix(path, "~/"))
			}
		}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read rubric file %q: %w", path, err)
	}
	r, err := ParseRubric(data)
	if err != nil {
		return nil, fmt.Errorf("parse rubric file %q: %w", path, err)
	}
	r.Source = "file:" + resolved
	return r, nil
}

// BuiltinRubricNames returns the names of all shipped rubrics, sorted.
func BuiltinRubricNames() []string {
	entries, err := builtinRubricsFS.ReadDir(builtinRubricsDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}

// LoadAllBuiltinRubrics returns every shipped rubric, parsed and validated.
func LoadAllBuiltinRubrics() ([]*Rubric, error) {
	names := BuiltinRubricNames()
	out := make([]*Rubric, 0, len(names))
	for _, n := range names {
		r, err := LoadBuiltinRubric(n)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// RenderPrompt composes the final judge prompt: the rubric body plus an
// injected section with the task input, optional source context, and candidate
// output. When all injection values are empty the rubric body is returned
// unchanged so that prompt graders running with continue_session (where the
// judge resumes the agent's session and already has the conversation) are not
// disturbed.
func (r *Rubric) RenderPrompt(taskInput, sourceContext, candidateOutput string) string {
	var sb strings.Builder
	sb.WriteString(r.Body)

	taskInput = strings.TrimSpace(taskInput)
	sourceContext = strings.TrimSpace(sourceContext)
	candidateOutput = strings.TrimSpace(candidateOutput)

	if taskInput == "" && sourceContext == "" && candidateOutput == "" {
		return sb.String()
	}

	sb.WriteString("\n\n---\n")
	if taskInput != "" {
		sb.WriteString("\n## Task input\n")
		sb.WriteString(taskInput)
		sb.WriteString("\n")
	}
	if sourceContext != "" {
		sb.WriteString("\n## Source context\n")
		sb.WriteString(sourceContext)
		sb.WriteString("\n")
	}
	if candidateOutput != "" {
		sb.WriteString("\n## Candidate output\n")
		sb.WriteString(candidateOutput)
		sb.WriteString("\n")
	}
	return sb.String()
}
