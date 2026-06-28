package specverify

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/microsoft/waza/internal/scaffold"
	"github.com/microsoft/waza/internal/skill"
	"gopkg.in/yaml.v3"
)

var parameterHeadingRE = regexp.MustCompile(`(?i)^#{1,6}\s+.*\b(parameters?|inputs?|arguments?)\b`)

// ParseSkillFile deterministically extracts requirements from a SKILL.md file.
func ParseSkillFile(path string) (*ParsedSkillSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SKILL.md: %w", err)
	}

	var sk skill.Skill
	if err := sk.UnmarshalText(data); err != nil {
		return nil, fmt.Errorf("parsing SKILL.md: %w", err)
	}
	if strings.TrimSpace(sk.Frontmatter.Name) == "" {
		sk.Frontmatter.Name = filepath.Base(filepath.Dir(path))
	}

	lines := splitLines(string(data))
	requirements := make([]Requirement, 0)

	description := strings.TrimSpace(sk.Frontmatter.Description)
	if description != "" {
		descSpan := descriptionSourceSpan(path, sk.FrontmatterNode, lines)
		descriptionText := descriptionRequirementText(description)
		if descriptionText != "" {
			requirements = append(requirements, Requirement{
				ID:     "req-description-001",
				Kind:   RequirementDescription,
				Text:   descriptionText,
				Source: descSpan,
			})
		}

		useFor, doNotUseFor := scaffold.ParseTriggerPhrases(description)
		for i, p := range useFor {
			text := strings.TrimSpace(p.Prompt)
			if text == "" {
				continue
			}
			requirements = append(requirements, Requirement{
				ID:     fmt.Sprintf("req-use-%03d", i+1),
				Kind:   RequirementUse,
				Text:   text,
				Source: findTextSpan(path, lines, descSpan, text),
			})
		}
		for i, p := range doNotUseFor {
			text := strings.TrimSpace(p.Prompt)
			if text == "" {
				continue
			}
			requirements = append(requirements, Requirement{
				ID:     fmt.Sprintf("req-dont-%03d", i+1),
				Kind:   RequirementDont,
				Text:   text,
				Source: findTextSpan(path, lines, descSpan, text),
			})
		}
	}

	params := parseFrontmatterParameters(path, sk.FrontmatterNode)
	params = append(params, parseBodyParameters(path, sk.Body, frontmatterEndLine(lines))...)
	for i, param := range params {
		param.ID = fmt.Sprintf("req-param-%03d", i+1)
		requirements = append(requirements, param)
	}

	return &ParsedSkillSpec{
		SkillName:    strings.TrimSpace(sk.Frontmatter.Name),
		SkillPath:    path,
		Requirements: requirements,
	}, nil
}

func parseFrontmatterParameters(path string, node *yaml.Node) []Requirement {
	if node == nil {
		return nil
	}
	mapping := yamlMapping(node)
	if mapping == nil {
		return nil
	}

	var params []Requirement
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key := strings.ToLower(strings.TrimSpace(mapping.Content[i].Value))
		if key != "parameters" && key != "params" && key != "inputs" {
			continue
		}
		value := mapping.Content[i+1]
		switch value.Kind {
		case yaml.SequenceNode:
			for _, item := range value.Content {
				text := nodeText(item)
				if text == "" {
					continue
				}
				params = append(params, Requirement{
					Kind:   RequirementParameter,
					Text:   text,
					Source: SourceSpan{File: path, StartLine: item.Line + 1, EndLine: item.Line + 1},
				})
			}
		case yaml.MappingNode:
			for j := 0; j+1 < len(value.Content); j += 2 {
				text := strings.TrimSpace(value.Content[j].Value)
				desc := nodeText(value.Content[j+1])
				if desc != "" {
					text += ": " + desc
				}
				params = append(params, Requirement{
					Kind:   RequirementParameter,
					Text:   text,
					Source: SourceSpan{File: path, StartLine: value.Content[j].Line + 1, EndLine: value.Content[j+1].Line + 1},
				})
			}
		case yaml.ScalarNode:
			text := strings.TrimSpace(value.Value)
			if text != "" {
				params = append(params, Requirement{
					Kind:   RequirementParameter,
					Text:   text,
					Source: SourceSpan{File: path, StartLine: value.Line + 1, EndLine: value.Line + 1},
				})
			}
		}
	}
	return params
}

func parseBodyParameters(path, body string, bodyStartLine int) []Requirement {
	lines := splitLines(body)
	var params []Requirement

	for i := 0; i < len(lines); i++ {
		if !parameterHeadingRE.MatchString(strings.TrimSpace(lines[i])) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			line := strings.TrimSpace(lines[j])
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "#") {
				break
			}
			if reqText, ok := parseParameterLine(line); ok {
				params = append(params, Requirement{
					Kind: RequirementParameter,
					Text: reqText,
					Source: SourceSpan{
						File:      path,
						StartLine: bodyStartLine + j,
						EndLine:   bodyStartLine + j,
					},
				})
			}
		}
	}
	return params
}

func parseParameterLine(line string) (string, bool) {
	if strings.HasPrefix(line, "|") && strings.HasSuffix(line, "|") {
		cells := splitMarkdownTableRow(line)
		if len(cells) < 2 || isMarkdownSeparatorRow(cells) {
			return "", false
		}
		key := cleanParameterName(cells[0])
		if key == "" || strings.EqualFold(key, "property") || strings.EqualFold(key, "parameter") || strings.EqualFold(key, "name") {
			return "", false
		}
		return key + ": " + strings.TrimSpace(cells[1]), true
	}

	if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
		text := strings.TrimSpace(line[2:])
		if key, rest, ok := strings.Cut(text, ":"); ok {
			key = cleanParameterName(key)
			rest = strings.TrimSpace(rest)
			if key != "" && rest != "" {
				return key + ": " + rest, true
			}
		}
		if key, rest, ok := strings.Cut(text, " - "); ok {
			key = cleanParameterName(key)
			rest = strings.TrimSpace(rest)
			if key != "" && rest != "" {
				return key + ": " + rest, true
			}
		}
		text = cleanParameterName(text)
		if text != "" {
			return text, true
		}
	}
	return "", false
}

func splitMarkdownTableRow(line string) []string {
	trimmed := strings.Trim(line, "|")
	raw := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(raw))
	for _, cell := range raw {
		cells = append(cells, strings.TrimSpace(cell))
	}
	return cells
}

func isMarkdownSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		clean := strings.Trim(cell, ":- ")
		if clean != "" {
			return false
		}
	}
	return true
}

func cleanParameterName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`* ")
	return s
}

func nodeText(node *yaml.Node) string {
	if node == nil {
		return ""
	}
	switch node.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(node.Value)
	case yaml.MappingNode:
		parts := make([]string, 0, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := strings.TrimSpace(node.Content[i].Value)
			value := nodeText(node.Content[i+1])
			if key != "" && value != "" {
				parts = append(parts, key+": "+value)
			}
		}
		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

func yamlMapping(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil
	}
	return node
}

func descriptionSourceSpan(path string, node *yaml.Node, lines []string) SourceSpan {
	mapping := yamlMapping(node)
	if mapping != nil {
		for i := 0; i+1 < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == "description" {
				start := mapping.Content[i].Line + 1
				end := mapping.Content[i+1].Line + 1
				if mapping.Content[i+1].Kind == yaml.ScalarNode && mapping.Content[i+1].Style == yaml.LiteralStyle {
					end = blockScalarEndLine(lines, start)
				}
				return SourceSpan{File: path, StartLine: start, EndLine: end}
			}
		}
	}
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "description:") {
			return SourceSpan{File: path, StartLine: i + 1, EndLine: i + 1}
		}
	}
	return SourceSpan{File: path, StartLine: 1, EndLine: 1}
}

func blockScalarEndLine(lines []string, start int) int {
	end := start
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		end = i + 1
	}
	return end
}

func findTextSpan(path string, lines []string, within SourceSpan, text string) SourceSpan {
	needle := normalizeForSearch(text)
	start := max(1, within.StartLine)
	end := min(len(lines), max(within.EndLine, within.StartLine))
	for i := start; i <= end; i++ {
		if strings.Contains(normalizeForSearch(lines[i-1]), needle) {
			return SourceSpan{File: path, StartLine: i, EndLine: i}
		}
	}
	for i, line := range lines {
		if strings.Contains(normalizeForSearch(line), needle) {
			return SourceSpan{File: path, StartLine: i + 1, EndLine: i + 1}
		}
	}
	return within
}

func frontmatterEndLine(lines []string) int {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return 1
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return i + 1
		}
	}
	return 1
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(s, "\n")
}

func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func descriptionRequirementText(description string) string {
	text := singleLine(description)
	upper := strings.ToUpper(text)
	end := len(text)
	for _, label := range []string{"USE FOR:", "DO NOT USE FOR:", "INVOKES:", "FOR SINGLE OPERATIONS:"} {
		if idx := strings.Index(upper, label); idx >= 0 && idx < end {
			end = idx
		}
	}
	return strings.TrimRight(strings.TrimSpace(text[:end]), ".")
}

func normalizeForSearch(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
