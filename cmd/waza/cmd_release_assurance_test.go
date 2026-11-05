package main

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestAssuranceExplicitSelectionRejectsBeforeIO(t *testing.T) {
	for _, factory := range []func() *cobra.Command{newCompareCommand, newGateCommand, newCompareCollectCommand} {
		for _, flag := range []string{"assurance-contract", "baseline-references", "candidate-review", "baseline-accept-review-source"} {
			cmd := factory()
			t.Run(cmd.Name()+"/"+flag, func(t *testing.T) {
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				args := []string{"--" + flag + "=", "--release-policy=does-not-exist", "--collection-dir=does-not-exist"}
				if cmd.Name() == "compare-collect" {
					args = append(args, "--baseline-eval=does-not-exist", "--candidate-eval=does-not-exist")
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				var exit *ExitCodeError
				require.True(t, errors.As(err, &exit), "%v", err)
				require.Equal(t, 1, exit.Code)
				require.NotContains(t, err.Error(), "no such file")
				require.False(t, shouldRunUpdateCheck(cmd, false))
			})
		}
	}
}

func TestAssuranceSourceOnlySelectionCannotFallThroughToLegacy(t *testing.T) {
	for _, cmd := range []*cobra.Command{newCompareCommand(), newGateCommand()} {
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--baseline-eval=does-not-exist"})
		err := cmd.Execute()
		var exit *ExitCodeError
		require.True(t, errors.As(err, &exit), "%v", err)
		require.Equal(t, 1, exit.Code)
		require.ErrorContains(t, err, "--assurance-contract")
		require.False(t, shouldRunUpdateCheck(cmd, false))
	}
}

func TestAssurancePlanDoesNotAcceptUnusedContract(t *testing.T) {
	cmd := newCompareAssurancePlanCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--assurance-contract=ignored"})
	require.ErrorContains(t, cmd.Execute(), "unknown flag")
}

func TestAssuranceExplicitEmptyOptionalInputs(t *testing.T) {
	for _, name := range []string{"baseline-review", "candidate-accept-review-source", "baseline-context-dir"} {
		cmd := newCompareAssurancePlanCommand()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--release-policy=does-not-exist", "--output=does-not-exist",
			"--baseline-eval=does-not-exist", "--candidate-eval=does-not-exist",
			"--baseline-references=does-not-exist", "--candidate-references=does-not-exist", "--" + name + "="})
		require.ErrorContains(t, cmd.Execute(), "cannot be empty")
	}
}

func TestAssuredRendererRejectsUnsupportedFormat(t *testing.T) {
	var out bytes.Buffer
	require.Error(t, renderAssuredDecision(&out, releasepolicy.AssuredDecision{}, "xml"))
}
