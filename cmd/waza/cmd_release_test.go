package main

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/spf13/cobra"
)

func TestSelectedReleasePolicyExitBoundary(t *testing.T) {
	for _, command := range []string{"gate", "compare"} {
		for _, args := range [][]string{
			{"--release-policy", ""},
			{"--collection-dir", ""},
			{"--release-policy", "absent-policy.json"},
			{"--collection-dir", "absent-collection"},
			{"--release-policy", "absent-policy.json", "--collection-dir", "absent-collection", "--format", "json"},
		} {
			t.Run(command+strings.Join(args, " "), func(t *testing.T) {
				var cmd *cobra.Command
				if command == "gate" {
					cmd = newGateCommand()
				} else {
					cmd = newCompareCommand()
				}
				cmd.SetArgs(args)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(io.Discard)
				err := cmd.Execute()
				var exit *ExitCodeError
				if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(out.String(), "invalid") {
					t.Fatalf("strict selection error=%v output=%s", err, out.String())
				}
			})
		}
	}
}

func TestSelectedReleaseCannotSilentlyIgnoreLegacyFlags(t *testing.T) {
	for _, flag := range []string{"baseline", "current", "max-regression-pct", "golden-must-pass", "on-new-tasks", "on-removed-tasks"} {
		t.Run(flag, func(t *testing.T) {
			cmd := newGateCommand()
			value := "unused"
			if flag == "golden-must-pass" {
				value = "false"
			}
			if flag == "max-regression-pct" {
				value = "5"
			}
			cmd.SetArgs([]string{"--release-policy", "unused-policy", "--collection-dir", "unused-collection", "--" + flag + "=" + value})
			cmd.SetErr(io.Discard)
			var exit *ExitCodeError
			if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 ||
				!strings.Contains(err.Error(), "cannot be combined") {
				t.Fatalf("ignored legacy flag: %v", err)
			}
		})
	}
	cmd := newCompareCommand()
	cmd.SetArgs([]string{"historical.json", "current.json", "--release-policy", "unused", "--collection-dir", "unused"})
	cmd.SetErr(io.Discard)
	var exit *ExitCodeError
	if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 {
		t.Fatalf("strict historical adoption was not rejected: %v", err)
	}
}

func TestSelectedReleaseRendering(t *testing.T) {
	d := releasepolicy.InitialDecision()
	d.Golden.State, d.Billing.State, d.Statistics.State = "missing_required_evidence", "unavailable", "underpowered"
	for _, format := range []string{"human", "json", "markdown", "github-actions"} {
		var out bytes.Buffer
		if err := renderControlledDecision(&out, d, format); err != nil {
			t.Fatal(err)
		}
		for _, state := range []string{"not_assessed", "missing_required_evidence", "unavailable", "underpowered"} {
			if !strings.Contains(out.String(), state) {
				t.Fatalf("%s lost distinct state %s: %s", format, state, out.String())
			}
		}
	}
	if err := renderControlledDecision(io.Discard, d, "unsupported"); err == nil {
		t.Fatal("unsupported format accepted")
	}
	if err := renderControlledDecision(alwaysFailWriter{}, d, "human"); err == nil {
		t.Fatal("write failure hidden")
	}
	var out bytes.Buffer
	err := runControlledAssessment(&out, filepath.Join(t.TempDir(), "missing"), "collection", "human")
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != 1 || !strings.Contains(out.String(), "invalid") {
		t.Fatalf("unreadable selected policy passed: %v", err)
	}
}

type alwaysFailWriter struct{}

func (alwaysFailWriter) Write([]byte) (int, error) { return 0, errors.New("test write error") }
