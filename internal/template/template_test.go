package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		ctx     *Context
		want    string
		wantErr bool
	}{
		{
			name: "system var TaskName",
			tmpl: "Running {{.TaskName}}",
			ctx:  &Context{TaskName: "code-explainer"},
			want: "Running code-explainer",
		},
		{
			name: "system var JobID",
			tmpl: "Job: {{.JobID}}",
			ctx:  &Context{JobID: "abc-123"},
			want: "Job: abc-123",
		},
		{
			name: "system var Iteration and Attempt",
			tmpl: "iter={{.Iteration}} attempt={{.Attempt}}",
			ctx:  &Context{Iteration: 3, Attempt: 2},
			want: "iter=3 attempt=2",
		},
		{
			name: "system var Timestamp",
			tmpl: "ts={{.Timestamp}}",
			ctx:  &Context{Timestamp: "2026-02-18T12:00:00Z"},
			want: "ts=2026-02-18T12:00:00Z",
		},
		{
			name: "user-defined Vars",
			tmpl: "Hello {{.Vars.username}}, role={{.Vars.role}}",
			ctx: &Context{
				Vars: map[string]string{
					"username": "alice",
					"role":     "admin",
				},
			},
			want: "Hello alice, role=admin",
		},
		{
			name: "no templates passthrough",
			tmpl: "plain string with no templates",
			ctx:  &Context{TaskName: "ignored"},
			want: "plain string with no templates",
		},
		{
			name: "empty string input",
			tmpl: "",
			ctx:  &Context{},
			want: "",
		},
		{
			name:    "missing system variable",
			tmpl:    "{{.NoSuchField}}",
			ctx:     &Context{},
			wantErr: true,
		},
		{
			name:    "missing Vars key",
			tmpl:    "{{.Vars.missing}}",
			ctx:     &Context{Vars: map[string]string{}},
			wantErr: true,
		},
		{
			name:    "nil Vars map with Vars access",
			tmpl:    "{{.Vars.key}}",
			ctx:     &Context{},
			wantErr: true,
		},
		{
			name: "complex expression with conditional",
			tmpl: `{{if eq .TaskName "test"}}YES{{else}}NO{{end}}`,
			ctx:  &Context{TaskName: "test"},
			want: "YES",
		},
		{
			name: "mixed system and user vars",
			tmpl: "{{.TaskName}}: {{.Vars.lang}} iter={{.Iteration}}",
			ctx: &Context{
				TaskName:  "compile",
				Iteration: 1,
				Vars:      map[string]string{"lang": "go"},
			},
			want: "compile: go iter=1",
		},
		{
			name:    "invalid template syntax",
			tmpl:    "bad {{.Unclosed",
			ctx:     &Context{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Render(tc.tmpl, tc.ctx)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "template:")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestRenderHookStrictIndexedVars(t *testing.T) {
	_, err := RenderHook(`{{index .Vars "deployment-env"}}`, &Context{Vars: map[string]string{}})
	require.ErrorContains(t, err, `map has no entry for key "deployment-env"`)

	got, err := RenderHook(
		`{{index .Vars "deployment-env"}}`,
		&Context{Vars: map[string]string{"deployment-env": ""}},
	)
	require.NoError(t, err)
	require.Empty(t, got)
}
