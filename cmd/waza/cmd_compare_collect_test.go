package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/spf13/cobra"
)

func TestControlledCLIWorkflow(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("new durable collection unsupported")
	}
	example := testutil.RepoFile(t, "examples", "controlled-comparison")
	dir := t.TempDir()
	policy := filepath.Join(dir, "policy.json")
	sources := []string{"--baseline-eval", filepath.Join(example, "eval.yaml"),
		"--candidate-eval", filepath.Join(example, "eval.yaml")}
	cmd := newComparePlanCommand()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(append([]string{}, sources...), "--design", filepath.Join(example, "design.json"),
		"--requirements", filepath.Join(example, "requirements.json"), "--output", policy))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(policy)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("policy not private: %v %v", info, err)
	}
	// Reusing the artifact path must not overwrite a precollection commitment.
	if err := cmd.Execute(); err == nil {
		t.Fatal("existing policy overwritten")
	}
	directory := filepath.Join(dir, "collection")
	collect := newCompareCollectCommand()
	collect.SetOut(io.Discard)
	collect.SetErr(io.Discard)
	collect.SetArgs(append(append([]string{}, sources...), "--release-policy", policy, "--collection-dir", directory))
	if err := collect.Execute(); err != nil {
		t.Fatal(err)
	}
	gate := newGateCommand()
	var out bytes.Buffer
	gate.SetOut(&out)
	gate.SetErr(io.Discard)
	gate.SetArgs([]string{"--release-policy", policy, "--collection-dir", directory, "--format", "json"})
	var exit *ExitCodeError
	if err := gate.Execute(); !errors.As(err, &exit) || exit.Code != 1 ||
		!strings.Contains(out.String(), `"inconclusive"`) || !strings.Contains(out.String(), `"accepted": false`) {
		t.Fatalf("one-cluster mock example improperly strict-passed: %v %s", err, out.String())
	}
}

func TestControlledCLIRequiredBoundaries(t *testing.T) {
	for _, cmd := range []*cobra.Command{newComparePlanCommand(), newCompareCollectCommand()} {
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		var exit *ExitCodeError
		if err := cmd.Execute(); !errors.As(err, &exit) || exit.Code != 1 {
			t.Fatalf("missing explicit inputs did not reject: %v", err)
		}
	}
	for _, args := range [][]string{{"compare-plan"}, {"compare-collect"}, {"compare", "--release-policy=policy"},
		{"gate", "--release-policy=policy"}, {"compare", "--collection-dir=collection"},
		{"gate", "--collection-dir=collection"}, {"compare", "--collection-dir="}, {"gate", "--collection-dir="}} {
		root := newRootCommand()
		root.SetArgs(args)
		command, _, err := root.Find(args)
		if err != nil {
			t.Fatal(err)
		}
		if len(args) > 1 {
			if err := command.ParseFlags(args[1:]); err != nil {
				t.Fatal(err)
			}
		}
		if shouldRunUpdateCheck(command, false) {
			t.Fatalf("explicit offline policy path enabled network update check: %v", args)
		}
	}
}
