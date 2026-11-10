package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestAggregateSavedCLIToPublicAPIReport(t *testing.T) {
	directory := t.TempDir()
	storeDir := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storeDir, 0o700))
	backend, err := artifactmanifest.OpenLocal(storeDir)
	require.NoError(t, err)
	source := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedZero)
	require.NoError(t, backend.Close())
	good := manifestaggregate.Input{Manifest: source.Manifest, Evidence: source.Evidence}
	missing := manifestaggregate.Input{Manifest: strings.Repeat("a", 64)}
	for _, control := range []struct {
		name       string
		inputs     []manifestaggregate.Input
		incomplete bool
	}{
		{"complete", []manifestaggregate.Input{good}, false},
		{"missing neighbor", []manifestaggregate.Input{missing, good}, true},
	} {
		t.Run(control.name, func(t *testing.T) {
			wire, err := json.Marshal(control.inputs)
			require.NoError(t, err)
			inputFile := filepath.Join(t.TempDir(), "inputs.json")
			outputFile := filepath.Join(t.TempDir(), "aggregate.json")
			require.NoError(t, os.WriteFile(inputFile, wire, 0o600))
			command := newAggregateCommand()
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&bytes.Buffer{})
			command.SetArgs([]string{"--store", storeDir, "--inputs", inputFile, "-o", outputFile})
			err = command.Execute()
			if control.incomplete {
				require.ErrorIs(t, err, manifestaggregate.ErrIncomplete)
			} else {
				require.NoError(t, err)
			}
			saved, err := os.ReadFile(outputFile)
			require.NoError(t, err, "evaluated incomplete reports must also be persisted")
			report, err := manifestaggregate.Parse(saved)
			require.NoError(t, err)
			require.Equal(t, len(control.inputs), report.Counts.Members)
			require.Equal(t, append(bytes.Clone(saved), '\n'), stdout.Bytes())
			require.Equal(t, !control.incomplete, report.Usage.Recorded != nil)
			mux := http.NewServeMux()
			webapi.RegisterAggregateRoutesWithSources(mux, func() (manifestaggregate.Sources, func() error, error) {
				return manifestaggregate.OpenLocal(storeDir)
			})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", bytes.NewReader(wire)))
			expectedStatus := http.StatusOK
			if control.incomplete {
				expectedStatus = http.StatusConflict
			}
			require.Equal(t, expectedStatus, response.Code)
			require.Equal(t, saved, response.Body.Bytes(), "actual CLI saved JSON and API canonical bytes must agree")
			retry := newAggregateCommand()
			retry.SetOut(&bytes.Buffer{})
			retry.SetErr(&bytes.Buffer{})
			retry.SetArgs([]string{"--store", storeDir, "--inputs", inputFile, "-o", outputFile})
			require.Error(t, retry.Execute(), "aggregate output must not replace an occupied file")
			preserved, err := os.ReadFile(outputFile)
			require.NoError(t, err)
			require.Equal(t, saved, preserved)
		})
	}
}

func TestAggregateInputAdmissionPrecedesStoreOpen(t *testing.T) {
	command := newAggregateCommand()
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--store", filepath.Join(t.TempDir(), "must-not-be-opened"), "invalid"})
	require.ErrorIs(t, command.Execute(), manifestaggregate.ErrInvalid)
}

func TestAggregateOutputRejectsLeafSymlinkAndKeepsExistingBytes(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "user-owned")
	require.NoError(t, os.WriteFile(target, []byte("unchanged"), 0o600))
	link := filepath.Join(directory, "aggregate.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	require.Error(t, writeAggregateOutput(link, []byte("new")))
	preserved, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "unchanged", string(preserved))
}
