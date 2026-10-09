package commandmock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestMatchResponseNativeSemantics(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WAZA_WRAPPER_MATCH_TEST", "")
	for _, test := range []struct {
		name     string
		response models.CommandMockResponse
		args     []string
		cwd      string
		want     bool
	}{
		{"empty args", models.CommandMockResponse{Args: []string{}}, nil, root, true},
		{"exact args", models.CommandMockResponse{Args: []string{"read"}}, []string{"read"}, root, true},
		{"exact mismatch", models.CommandMockResponse{Args: []string{"read"}}, []string{"other"}, root, false},
		{"args length", models.CommandMockResponse{Args: []string{"read"}}, nil, root, false},
		{"regex anchored", models.CommandMockResponse{ArgsRegex: []string{"read"}}, []string{"prefix-read"}, root, false},
		{"regex match", models.CommandMockResponse{ArgsRegex: []string{"r.*"}}, []string{"read"}, root, true},
		{"regex length", models.CommandMockResponse{ArgsRegex: []string{"r.*"}}, nil, root, false},
		{"empty environment present", models.CommandMockResponse{Args: []string{}, Environment: map[string]string{"WAZA_WRAPPER_MATCH_TEST": ""}}, nil, root, true},
		{"environment value mismatch", models.CommandMockResponse{Args: []string{}, Environment: map[string]string{"WAZA_WRAPPER_MATCH_TEST": "yes"}}, nil, root, false},
		{"workdir normalization", models.CommandMockResponse{Args: []string{}, WorkDir: "sub/./dir"}, nil, filepath.Join(root, "sub", "dir"), true},
		{"explicit root workdir", models.CommandMockResponse{Args: []string{}, WorkDir: "."}, nil, root, true},
		{"workdir mismatch", models.CommandMockResponse{Args: []string{}, WorkDir: "."}, nil, filepath.Join(root, "sub"), false},
		{"omitted workdir", models.CommandMockResponse{Args: []string{}}, nil, filepath.Join(root, "sub"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := MatchResponse(test.response, test.args, test.cwd, root)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	require.NoError(t, os.Unsetenv("WAZA_WRAPPER_MATCH_TEST"))
	got, err := MatchResponse(models.CommandMockResponse{Args: []string{}, Environment: map[string]string{"WAZA_WRAPPER_MATCH_TEST": ""}}, nil, root, root)
	require.NoError(t, err)
	require.False(t, got)
	_, err = MatchResponse(models.CommandMockResponse{ArgsRegex: []string{"["}}, []string{"secret"}, root, root)
	require.ErrorContains(t, err, "invalid command-mock regex")
	require.NotContains(t, err.Error(), "secret")
}

func TestResponseOutputNativeSemantics(t *testing.T) {
	base := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(base, "fixture.bin"), []byte{0, 255}, 0600))
	for _, test := range []struct {
		name     string
		response models.CommandMockResponse
		want     []byte
	}{
		{"omitted", models.CommandMockResponse{}, nil},
		{"empty string", models.CommandMockResponse{Stdout: ""}, []byte{}},
		{"malformed string preserved", models.CommandMockResponse{Stdout: "{not json"}, []byte("{not json")},
		{"JSON value", models.CommandMockResponse{Stdout: map[string]any{"ready": true}}, []byte(`{"ready":true}`)},
		{"fixture raw bytes", models.CommandMockResponse{Fixture: "fixture.bin"}, []byte{0, 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResponseOutput(test.response, base)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	_, err := ResponseOutput(models.CommandMockResponse{Fixture: "absent"}, base)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = ResponseOutput(models.CommandMockResponse{Stdout: func() {}}, base)
	require.ErrorContains(t, err, "encoding stdout as JSON")
	var unsupported *json.UnsupportedTypeError
	require.ErrorAs(t, err, &unsupported)
}
