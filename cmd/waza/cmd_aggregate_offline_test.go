package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type aggregateNetworkGuard struct {
	calls     atomic.Int64
	contacted chan struct{}
}

func (g *aggregateNetworkGuard) RoundTrip(*http.Request) (*http.Response, error) {
	g.calls.Add(1)
	select {
	case g.contacted <- struct{}{}:
	default:
	}
	return nil, errors.New("network forbidden in aggregate offline control")
}

func TestAggregateUpdateCheckOnlyExcludesExactCommandAndAncestors(t *testing.T) {
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	root := newRootCommand()
	for _, control := range []struct {
		name string
		want bool
	}{{"aggregate", false}, {"models", true}, {"results", true}, {"artifact", true}, {"runner", false}, {"preflight", false}} {
		command, _, err := root.Find([]string{control.name})
		require.NoError(t, err)
		require.Equal(t, control.name, command.Name())
		require.Equal(t, control.want, shouldRunUpdateCheck(command, false), control.name)
	}
	aggregate, _, err := root.Find([]string{"aggregate"})
	require.NoError(t, err)
	child := &cobra.Command{Use: "child"}
	aggregate.AddCommand(child)
	require.False(t, shouldRunUpdateCheck(child, false))
	require.True(t, shouldRunUpdateCheck(&cobra.Command{Use: "aggregate-other"}, false))
	require.True(t, shouldRunUpdateCheck(root, false))
}

func TestAggregateRegisteredRootIsOfflineWithoutUpdateOptOut(t *testing.T) {
	directory := t.TempDir()
	store := filepath.Join(directory, "store")
	require.NoError(t, os.Mkdir(store, 0o700))
	backend, err := artifactmanifest.OpenLocal(store)
	require.NoError(t, err)
	source := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedNonzero(1))
	require.NoError(t, backend.Close())
	t.Chdir(directory)
	require.NoError(t, os.WriteFile(".waza.yaml", []byte("invalid: [ambient configuration must not be read"), 0o600))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	oldVersion := version
	version = "0.1.0"
	t.Cleanup(func() { version = oldVersion })
	guard := &aggregateNetworkGuard{contacted: make(chan struct{}, 1)}
	transport := http.DefaultTransport
	http.DefaultTransport = guard
	t.Cleanup(func() { http.DefaultTransport = transport })
	output := filepath.Join(directory, "saved.json")
	root := newRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"aggregate", "--store", store, source.Manifest, "-o", output})
	require.NoError(t, root.Execute())
	require.False(t, root.PersistentFlags().Lookup("no-update-check").Changed)
	require.Equal(t, "false", root.PersistentFlags().Lookup("no-update-check").Value.String())
	saved, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, append(bytes.Clone(saved), '\n'), stdout.Bytes())
	report, err := manifestaggregate.Parse(saved)
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.Equal(t, 1, report.Counts.VerifiedManifests)
	require.Equal(t, source.Manifest, report.Members[0].Manifest)
	require.Empty(t, stderr.String())
	require.Zero(t, guard.calls.Load(), "registered root must not start its update checker")
	require.NoDirExists(t, filepath.Join(home, ".waza"))

	// Prove the same unsuppressed root hook can reach the rejecting transport.
	control := newRootCommand()
	models, _, err := control.Find([]string{"models"})
	require.NoError(t, err)
	require.True(t, shouldRunUpdateCheck(models, false))
	control.PersistentPreRun(models, nil)
	select {
	case <-guard.contacted:
	case <-time.After(5 * time.Second):
		t.Fatal("released-version, fresh-home update-check positive control did not reach transport")
	}
	control.PersistentPostRun(models, nil)
	require.EqualValues(t, 1, guard.calls.Load())
}
