package template

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Context holds all variables available for template resolution.
type Context struct {
	// System variables
	JobID     string
	TaskName  string
	Iteration int
	Attempt   int
	Timestamp string

	// User-defined variables (from inputs section or CSV)
	Vars map[string]string
}

// Render resolves template expressions in the given string.
// Uses Go's text/template syntax: {{.TaskName}}, {{.Vars.myvar}}.
// Returns the input unchanged if it contains no template delimiters.
func Render(tmpl string, ctx *Context) (string, error) {
	// Fast path: no template delimiters means no work to do.
	if !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}

	t, err := template.New("").Funcs(template.FuncMap{
		"index": strictStringMapIndex,
	}).Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("template: parse: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("template: render: %w", err)
	}

	return buf.String(), nil
}

func strictStringMapIndex(values map[string]string, key string) (string, error) {
	value, ok := values[key]
	if !ok {
		return "", fmt.Errorf("map has no entry for key %q", key)
	}
	return value, nil
}
