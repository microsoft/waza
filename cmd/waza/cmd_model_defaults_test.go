package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestCommandModelDefaultsResolvedAtExecution(t *testing.T) {
	for _, command := range []struct {
		name    string
		create  func() *cobra.Command
		factory *func(string) execution.AgentEngine
	}{
		{"quality", newQualityCommand, &newQualityEngine},
		{"suggest", newSuggestCommand, &newSuggestEngine},
	} {
		for _, control := range []string{"builtin", "configured", "explicit", "explicit empty"} {
			t.Run(command.name+"/"+control, func(t *testing.T) {
				root := t.TempDir()
				t.Chdir(root)
				cmd := command.create()
				args := []string{"unused-skill"}
				expected := projectconfig.New().Defaults.Model
				if control != "builtin" {
					require.NoError(t, os.WriteFile(filepath.Join(root, ".waza.yaml"),
						[]byte("defaults:\n  model: configured-model\n"), 0o600))
					expected = "configured-model"
				}
				if control == "explicit" || control == "explicit empty" {
					expected = "explicit-model"
					if control == "explicit empty" {
						expected = ""
					}
					args = append(args, "--model="+expected)
				}
				stopped := errors.New("stop before inference")
				original := *command.factory
				t.Cleanup(func() { *command.factory = original })
				calls := 0
				*command.factory = func(model string) execution.AgentEngine {
					calls++
					require.Equal(t, expected, model)
					return &qualityTestEngine{initErr: stopped}
				}
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				require.ErrorIs(t, cmd.Execute(), stopped)
				require.Equal(t, 1, calls, "the selected model must reach the production engine factory")
			})
		}
	}
}
