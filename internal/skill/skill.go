// skill parses SKILL.md files
package skill

import (
	"encoding"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/spboyer/waza/internal/tokens"
	"gopkg.in/yaml.v3"
)

var (
	_ encoding.TextMarshaler   = (*Skill)(nil)
	_ encoding.TextUnmarshaler = (*Skill)(nil)
)

// Frontmatter holds parsed YAML frontmatter from SKILL.md.
type Frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Skill represents the current state of a skill for scoring.
type Skill struct {
	Frontmatter     Frontmatter
	FrontmatterRaw  map[string]any
	FrontmatterNode *yaml.Node
	Body            string
	Path            string
	RawContent      string
	Tokens          int
	Characters      int
	Lines           int
}

// parseFrontmatter splits YAML frontmatter (delimited by ---) from body.
func parseFrontmatter(content string) (Frontmatter, map[string]any, *yaml.Node, string, error) {
	var fm Frontmatter

	if !strings.HasPrefix(content, "---") {
		// No frontmatter — return empty fields with the whole content as body.
		return fm, nil, nil, content, nil
	}

	// Find the closing ---
	rest := content[3:]
	if strings.HasPrefix(rest, "\r\n") {
		rest = rest[2:]
	} else if strings.HasPrefix(rest, "\n") {
		rest = rest[1:]
	}

	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, nil, nil, content, errors.New("closing frontmatter delimiter not found")
	}

	yamlBlock := rest[:idx]
	body := rest[idx+4:] // skip \n---

	var rawFrontmatter map[string]any
	if err := yaml.Unmarshal([]byte(yamlBlock), &rawFrontmatter); err != nil {
		return fm, nil, nil, content, fmt.Errorf("unmarshalling frontmatter: %w", err)
	}
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return fm, nil, nil, content, fmt.Errorf("unmarshalling frontmatter: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(yamlBlock), &node); err != nil {
		return fm, nil, nil, content, fmt.Errorf("unmarshalling frontmatter: %w", err)
	}

	return fm, rawFrontmatter, &node, body, nil
}

func (s *Skill) MarshalText() ([]byte, error) {
	var fmBytes []byte
	var err error
	if s.FrontmatterNode != nil {
		updateFrontmatterNode(s.FrontmatterNode, "name", s.Frontmatter.Name)
		updateFrontmatterNode(s.FrontmatterNode, "description", s.Frontmatter.Description)
		fmBytes, err = yaml.Marshal(s.FrontmatterNode)
	} else {
		fmMap := make(map[string]any)
		if s.FrontmatterRaw != nil {
			maps.Copy(fmMap, s.FrontmatterRaw)
		}
		fmMap["name"] = s.Frontmatter.Name
		fmMap["description"] = s.Frontmatter.Description
		fmBytes, err = yaml.Marshal(&fmMap)
	}
	if err != nil {
		return nil, fmt.Errorf("marshaling frontmatter: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("---\n")
	buf.Write(fmBytes)
	buf.WriteString("---")
	buf.WriteString(s.Body)
	return []byte(buf.String()), nil
}

func (s *Skill) UnmarshalText(text []byte) error {
	raw := string(text)
	if strings.TrimSpace(raw) == "" {
		return errors.New("SKILL.md is empty")
	}

	fm, rawFrontmatter, node, body, err := parseFrontmatter(raw)
	if err != nil {
		return fmt.Errorf("parsing frontmatter: %w", err)
	}

	s.Frontmatter = fm
	s.FrontmatterRaw = rawFrontmatter
	s.FrontmatterNode = node
	s.Body = body
	s.RawContent = raw
	s.Tokens = tokens.Estimate(raw)
	s.Characters = len(raw)
	s.Lines = strings.Count(raw, "\n") + 1
	return nil
}

func updateFrontmatterNode(node *yaml.Node, key, value string) {
	if node == nil {
		return
	}
	current := node
	if current.Kind == yaml.DocumentNode && len(current.Content) > 0 {
		current = current.Content[0]
	}
	if current.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(current.Content); i += 2 {
		if current.Content[i].Value == key {
			current.Content[i+1].Kind = yaml.ScalarNode
			current.Content[i+1].Tag = "!!str"
			current.Content[i+1].Value = value
			return
		}
	}
	current.Content = append(current.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}
