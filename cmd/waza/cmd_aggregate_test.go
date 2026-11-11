package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
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
			status, body := postAggregateAPI(t, storeDir, control.inputs)
			expectedStatus := http.StatusOK
			if control.incomplete {
				expectedStatus = http.StatusConflict
			}
			require.Equal(t, expectedStatus, status)
			require.Equal(t, saved, body, "actual CLI saved JSON and API canonical bytes must agree")
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

func TestAggregateDocumentedInputBuilderUsesActualPublicReferences(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	source := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, nil)
	require.NoError(t, backend.Close())
	document, err := os.ReadFile("../../site/src/content/docs/guides/manifest-aggregation.mdx")
	require.NoError(t, err)
	t.Logf("native checkout newline bytes: LF=%d CRLF=%d", bytes.Count(document, []byte("\n")), bytes.Count(document, []byte("\r\n")))
	normalized := bytes.ReplaceAll(document, []byte("\r\n"), []byte("\n"))
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("line-ending-%q", ending), func(t *testing.T) {
			fixture := bytes.ReplaceAll(normalized, []byte("\n"), []byte(ending))
			require.Equal(t, 1, bytes.Count(fixture, []byte("node --input-type=module <<'JS'"+ending)))
			require.Equal(t, 1, bytes.Count(fixture, []byte(ending+"JS"+ending)))
			snippet, err := aggregateInputBuilderScript(string(fixture))
			require.NoError(t, err)
			builderDir := t.TempDir()
			builder := exec.Command("node", "--input-type=module", "-e", snippet)
			builder.Dir = builderDir
			builder.Env = append(os.Environ(), "MANIFEST_SHA256="+source.Manifest, "EVIDENCE_SHA256="+source.Evidence)
			output, err := builder.CombinedOutput()
			require.NoError(t, err, string(output))
			created, err := os.ReadFile(filepath.Join(builderDir, "aggregate-inputs.json"))
			require.NoError(t, err)
			inputs, err := manifestaggregate.ParseInputs(created)
			require.NoError(t, err)
			require.Equal(t, []manifestaggregate.Input{{Manifest: source.Manifest, Evidence: source.Evidence}}, inputs)
			builder = exec.Command("node", "--input-type=module", "-e", snippet)
			builder.Dir = builderDir
			builder.Env = append(os.Environ(), "MANIFEST_SHA256="+source.Manifest, "EVIDENCE_SHA256="+source.Evidence)
			_, err = builder.CombinedOutput()
			require.Error(t, err, "documented builder must not replace an existing input set")
			command := newAggregateCommand()
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs([]string{"--store", directory, "--inputs", filepath.Join(builderDir, "aggregate-inputs.json")})
			require.NoError(t, command.Execute(), "copied documentation must work through the actual aggregate command")
		})
	}
}

func aggregateInputBuilderScript(document string) (string, error) {
	openers := regexp.MustCompile(`(?m)^node --input-type=module <<'JS'\r?\n`).FindAllStringIndex(document, -1)
	if len(openers) != 1 {
		return "", fmt.Errorf("expected exactly one documented builder opener")
	}
	snippet := document[openers[0][1]:]
	closing := regexp.MustCompile(`\r?\nJS\r?\n`).FindStringIndex(snippet)
	if closing == nil {
		return "", fmt.Errorf("missing documented builder terminator")
	}
	return snippet[:closing[0]], nil
}

func TestAggregateDocumentedInputBuilderBoundaries(t *testing.T) {
	const opener = "node --input-type=module <<'JS'"
	for _, ending := range []string{"\n", "\r\n"} {
		script := "console.log('unchanged');" + ending + "// exact script bytes"
		document := opener + ending + script + ending + "JS" + ending
		actual, err := aggregateInputBuilderScript(document)
		require.NoError(t, err)
		require.Equal(t, script, actual, "only delimiters may be removed")
		for _, invalid := range []string{
			"missing opener" + ending + script + ending + "JS" + ending,
			document + document,
			opener + "near-miss" + ending + script + ending + "JS" + ending,
			opener + ending + script,
		} {
			_, err := aggregateInputBuilderScript(invalid)
			require.Error(t, err)
		}
	}
}
