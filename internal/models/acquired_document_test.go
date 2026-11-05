package models

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func acquiredDocumentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestAcquiredOfflineDocumentCanonicalDecodersAndOwnership(t *testing.T) {
	for _, kind := range []string{"eval", "task"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("id: task\nname: Native task\ninputs:\n  prompt: exact input\n")
			if kind == "eval" {
				data = []byte("schemaVersion: '1.0'\nname: native\nskill: test\nconfig:\n  executor: mock\n  model: test\n  trials_per_task: 1\n  timeout_seconds: 10\ntasks: [task.yaml]\n")
			}
			name := filepath.Join(t.TempDir(), kind+".yaml")
			require.NoError(t, os.WriteFile(name, data, 0o600))
			acquired, err := AcquireOfflineDocument(name, acquiredDocumentSHA(data), data)
			require.NoError(t, err)
			if kind == "eval" {
				control, err := LoadEvalSpecOffline(name)
				require.NoError(t, err)
				for index := range data {
					data[index] = 'x'
				}
				require.NoError(t, os.Remove(name))
				actual, err := LoadAcquiredEvalSpec(acquired)
				require.NoError(t, err)
				require.Equal(t, control, actual)
				_, err = LoadEvalSpecOffline(name)
				require.Error(t, err)
			} else {
				control, err := LoadTestCaseOffline(name)
				require.NoError(t, err)
				for index := range data {
					data[index] = 'x'
				}
				require.NoError(t, os.WriteFile(name, []byte("invalid: ["), 0o600))
				actual, err := LoadAcquiredTestCase(acquired)
				require.NoError(t, err)
				require.Equal(t, control, actual)
				_, err = LoadTestCaseOffline(name)
				require.Error(t, err)
			}
		})
	}
}

func TestAcquiredOfflineDocumentAdmissionAndDecoderErrors(t *testing.T) {
	for _, test := range []struct {
		name, label, expected string
		data                  []byte
	}{
		{"missing-label", "", acquiredDocumentSHA([]byte("data")), []byte("data")},
		{"missing-hash", "label", "", []byte("data")},
		{"empty", "label", acquiredDocumentSHA(nil), nil},
		{"oversize", "label", strings.Repeat("0", 64), make([]byte, 16*1024*1024+1)},
		{"mismatch", "label", strings.Repeat("0", 64), []byte("data")},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := AcquireOfflineDocument(test.label, test.expected, test.data)
			require.Error(t, err)
			require.Nil(t, document)
		})
	}
	_, err := LoadAcquiredEvalSpec(nil)
	require.Error(t, err)
	_, err = LoadAcquiredTestCase(nil)
	require.Error(t, err)
	for _, data := range [][]byte{[]byte("invalid: ["), []byte("name: missing-fields\n")} {
		document, err := AcquireOfflineDocument("not-opened", acquiredDocumentSHA(data), data)
		require.NoError(t, err)
		_, err = LoadAcquiredEvalSpec(document)
		require.Error(t, err)
		_, err = LoadAcquiredTestCase(document)
		require.Error(t, err)
	}
}

func TestAcquiredTaskRefusesPromptFileButOrdinaryPathPreservesIt(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "task.yaml")
	data := []byte("id: task\nname: Native task\ninputs:\n  prompt_file: prompt.txt\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("relative prompt"), 0o600))
	require.NoError(t, os.WriteFile(name, data, 0o600))
	control, err := LoadTestCaseOffline(name)
	require.NoError(t, err)
	require.Equal(t, "relative prompt", control.Stimulus.Message)
	document, err := AcquireOfflineDocument(name, acquiredDocumentSHA(data), bytes.Clone(data))
	require.NoError(t, err)
	_, err = LoadAcquiredTestCase(document)
	require.ErrorContains(t, err, "does not support prompt_file")
}

func TestAcquiredTaskRequiresIdentityWithoutChangingOrdinaryLoading(t *testing.T) {
	for _, declaration := range []string{"", "id: ''\n", "id: '  '\n"} {
		t.Run(declaration, func(t *testing.T) {
			data := []byte(declaration + "name: Native task\ninputs: {prompt: exact input}\n")
			name := filepath.Join(t.TempDir(), "task.yaml")
			require.NoError(t, os.WriteFile(name, data, 0o600))
			_, err := LoadTestCaseOffline(name)
			require.NoError(t, err, "ordinary task loading retains its existing identity defaults")
			document, err := AcquireOfflineDocument(name, acquiredDocumentSHA(data), data)
			require.NoError(t, err)
			task, err := LoadAcquiredTestCase(document)
			require.ErrorContains(t, err, "requires an explicit nonblank id")
			require.Nil(t, task)
		})
	}
}
