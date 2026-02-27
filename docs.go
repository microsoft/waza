package waza

import (
	"embed"
	"fmt"
	"path"
	"strings"
)

//go:embed docs/graders/*.md
var GraderDocsFS embed.FS

// GraderDocs returns grader documentation keyed by grader kind (e.g. "code", "regex").
// It reads from the embedded docs/graders/*.md files, skipping the README.
func GraderDocs() map[string]string {
	entries, err := GraderDocsFS.ReadDir("docs/graders")
	if err != nil {
		return nil
	}
	docs := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.EqualFold(name, "README.md") {
			continue
		}
		data, err := GraderDocsFS.ReadFile(path.Join("docs", "graders", name))
		if err != nil {
			continue
		}
		kind := strings.TrimSuffix(name, ".md")
		docs[kind] = string(data)
	}
	return docs
}

// FormatGraderDocs formats a subset of grader docs for inclusion in a prompt.
// If kinds is nil or empty, all docs are included.
func FormatGraderDocs(docs map[string]string, kinds []string) string {
	if len(docs) == 0 {
		return ""
	}
	selected := kinds
	if len(selected) == 0 {
		selected = make([]string, 0, len(docs))
		for k := range docs {
			selected = append(selected, k)
		}
	}
	var b strings.Builder
	for _, kind := range selected {
		content, ok := docs[kind]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "--- %s grader ---\n%s\n\n", kind, strings.TrimSpace(content))
	}
	return b.String()
}
