package releasepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

// These are authored artifact fixtures, not durable-writer qualification.
func authoredRootCollection(t *testing.T, p *Policy, complete bool) string {
	t.Helper()
	return authoredRootCollectionWithStatus(t, p, complete, "passed")
}

func authoredRootCollectionWithStatus(t *testing.T, p *Policy, complete bool, status string) string {
	t.Helper()
	directory := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), data, 0o600))
	}
	write("policy.json", testBytes(t, p))
	events := retryTestEvents(t, p, status)
	if !complete {
		write("journal.ndjson", retryTestStream(t, events[:4]))
		return directory
	}
	stream := retryTestStream(t, events)
	rawJournal := retryTestJournal(t, p, events, stream)
	write("journal.ndjson", stream)
	write("journal.json", rawJournal)
	journal, err := DecodeJournal(rawJournal, p)
	require.NoError(t, err)
	receipts, err := Replay(p, journal)
	require.NoError(t, err)
	for _, arm := range []Arm{Baseline, Candidate} {
		receipt := receipts[arm]
		begin := Receipt{Kind: ReceiptKind, Version: Version, State: "begin",
			CollectionID: receipt.CollectionID, PolicyDigest: p.Digest, Arm: arm,
			EvalID: receipt.EvalID, PlanDigest: receipt.PlanDigest, Samples: receipt.Samples}
		data, err := SealJSON(begin)
		require.NoError(t, err)
		write(string(arm)+".begin.json", data)
		data, err = SealJSON(receipt)
		require.NoError(t, err)
		write(string(arm)+".final.json", data)
		data, err = json.Marshal(receipt.Binding)
		require.NoError(t, err)
		write(string(arm)+".result-binding.json", data)
		result := Results{Kind: "waza.release-results", Version: Version,
			CollectionID: receipt.CollectionID, Arm: arm, EvalID: receipt.EvalID, Rows: []ActualRunRow{}}
		for _, attempt := range receipt.Binding.Attempts {
			observation, err := syntheticObservation(attempt)
			require.NoError(t, err)
			result.Rows = append(result.Rows, observation.Result)
		}
		data, err = json.Marshal(result)
		require.NoError(t, err)
		write(string(arm)+".results.json", data)
	}
	return directory
}

func TestRetainedDecisionRootCompleteAndPartial(t *testing.T) {
	for _, complete := range []bool{true, false} {
		name := "partial"
		if complete {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			p := testPolicy(t, 8)
			directory := authoredRootCollection(t, p, complete)
			expected, expectedErr := ReadDecision(directory)
			require.Equal(t, complete, expected.Accepted)
			root, err := os.OpenRoot(directory)
			require.NoError(t, err)
			moved := directory + "-retained"
			t.Cleanup(func() {
				closeErr := root.Close()
				removeErr := os.RemoveAll(moved)
				require.NoError(t, errors.Join(closeErr, removeErr))
			})
			actual, actualErr := ReadDecisionRootContext(t.Context(), root)
			require.Equal(t, expected, actual)
			if expectedErr == nil {
				require.NoError(t, actualErr)
			} else {
				require.EqualError(t, actualErr, expectedErr.Error())
			}
			_, err = root.Stat("policy.json")
			require.NoError(t, err, "borrowed root must remain open")
			renameErr := os.Rename(directory, moved)
			pinned := runtime.GOOS == "windows" && renameErr != nil
			if pinned {
				require.ErrorIs(t, renameErr, syscall.Errno(32), "only Windows sharing violation is an expected pin")
				_, err := os.Stat(directory)
				require.NoError(t, err)
				_, err = os.Stat(moved)
				require.ErrorIs(t, err, os.ErrNotExist)
				actual, actualErr = ReadDecisionRootContext(t.Context(), root)
				require.Equal(t, expected, actual, "pinned full/partial reads must retain the exact original decision")
				if expectedErr == nil {
					require.NoError(t, actualErr)
				} else {
					require.EqualError(t, actualErr, expectedErr.Error())
				}
				require.NoError(t, root.Close())
				require.NoError(t, os.Rename(directory, moved), "closing the caller-owned root must release the pin")
			} else {
				require.NoError(t, renameErr)
			}
			require.NoError(t, os.Mkdir(directory, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), []byte(`{"replacement":true}`), 0o600))
			actual, actualErr = ReadDecisionRootContext(t.Context(), root)
			if pinned {
				require.ErrorIs(t, actualErr, os.ErrClosed)
				require.False(t, actual.Accepted, "closed borrowed roots must not reopen the replacement")
			} else {
				require.Equal(t, expected, actual, "renamed handle must not reopen replacement directory")
				if expectedErr == nil {
					require.NoError(t, actualErr)
				} else {
					require.EqualError(t, actualErr, expectedErr.Error())
				}
			}
			replacement, err := ReadDecision(directory)
			require.Error(t, err)
			require.False(t, replacement.Accepted)
		})
	}
}

func TestRetainedSelectedDecisionRootBindsSelectedPolicy(t *testing.T) {
	policy := testPolicy(t, 8)
	root, err := os.OpenRoot(authoredRootCollection(t, policy, true))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })

	decision, err := ReadSelectedDecisionRootContext(t.Context(), root, policy.Digest)
	require.NoError(t, err)
	require.True(t, decision.Accepted)

	decision, err = ReadSelectedDecisionRootContext(t.Context(), root, testPolicy(t, 12).Digest)
	require.ErrorContains(t, err, "selected policy identity differs")
	require.False(t, decision.Accepted)
	require.Equal(t, "mismatched", decision.Compatibility.State)
}

func TestRetainedCoreDecisionRootBindsOriginalPolicyIdentity(t *testing.T) {
	policy := testPolicy(t, 8)
	policy.CollectionProfile = NativeCollectionProfile
	policy = sealTestPolicy(t, policy)
	directory := authoredRootCollection(t, policy, true)
	original, err := os.ReadFile(filepath.Join(directory, "policy.json"))
	require.NoError(t, err)
	require.NoError(t, os.Rename(filepath.Join(directory, "policy.json"), filepath.Join(directory, "core-policy.json")))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	for _, tc := range []struct {
		name     string
		selected models.EvidenceDigest
	}{
		{"matching", policy.Digest},
		{"wrong", testDigest(t, "different admitted envelope")},
		{"missing", models.EvidenceDigest{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := ReadCoreDecisionRootContext(t.Context(), root, tc.selected)
			if tc.name == "matching" {
				require.NoError(t, err)
				require.True(t, decision.Accepted)
				return
			}
			require.ErrorContains(t, err, "selected policy identity differs")
			require.False(t, decision.Accepted)
			require.Equal(t, "mismatched", decision.Compatibility.State)
		})
	}
	for _, wire := range []string{"omitted", "null", "wrong type"} {
		t.Run(wire, func(t *testing.T) {
			object, err := objectJSON(original)
			require.NoError(t, err)
			switch wire {
			case "omitted":
				delete(object, "requirements")
			case "null":
				object["requirements"] = nil
			case "wrong type":
				object["requirements"] = "not an object"
			}
			raw, err := SealJSON(object)
			require.NoError(t, err)
			var selected struct {
				Digest models.EvidenceDigest `json:"digest"`
			}
			require.NoError(t, json.Unmarshal(raw, &selected))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "core-policy.json"), raw, 0o600))
			decision, err := ReadCoreDecisionRootContext(t.Context(), root, selected.Digest)
			require.Error(t, err, "the selected identity must not replace original wire admission")
			require.False(t, decision.Accepted)
			require.Equal(t, "invalid", decision.Compatibility.State)
		})
	}
	var whitespace bytes.Buffer
	require.NoError(t, json.Indent(&whitespace, original, "", "  "))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "core-policy.json"), whitespace.Bytes(), 0o600))
	decision, err := ReadCoreDecisionRootContext(t.Context(), root, policy.Digest)
	require.NoError(t, err)
	require.True(t, decision.Accepted, "original JSON-v1 identity is not a source-byte identity")
}

func TestRetainedDecisionRootInvalidAndCancellation(t *testing.T) {
	d, err := ReadDecisionRootContext(t.Context(), nil)
	require.ErrorContains(t, err, "root is nil")
	require.False(t, d.Accepted)
	root, err := os.OpenRoot(authoredRootCollection(t, testPolicy(t, 8), true))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	d, err = ReadDecisionRootContext(ctx, root)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, d.Accepted)
	d, err = ReadDecisionRootContext(&cancelDuringArtifactReads{Context: t.Context()}, root)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, d.Accepted)
	require.NoError(t, root.Close())
	d, err = ReadDecisionRootContext(t.Context(), root)
	require.ErrorIs(t, err, os.ErrClosed)
	require.False(t, d.Accepted)
	d, err = ReadSelectedDecisionContext(ctx, "missing", testDigest(t, "selection"))
	require.ErrorIs(t, err, context.Canceled, "cancellation precedes opening")
	require.False(t, d.Accepted)
}

func TestRetainedDecisionRootReadBoundaryAndMixedArtifacts(t *testing.T) {
	p := testPolicy(t, 8)
	directory := authoredRootCollection(t, p, true)
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	d, err := readDecision(ctx, func(name string) ([]byte, error) {
		calls++
		data, err := readArtifactRootContext(ctx, root, name)
		if name == "candidate.results.json" {
			cancel()
		}
		return data, err
	}, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, d.Accepted)
	require.Greater(t, calls, 2)
	other := testPolicy(t, 12)
	otherDirectory := authoredRootCollection(t, other, true)
	data, err := os.ReadFile(filepath.Join(otherDirectory, "baseline.final.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "baseline.final.json"), data, 0o600))
	d, err = ReadDecisionRootContext(t.Context(), root)
	require.Error(t, err)
	require.False(t, d.Accepted)
	require.Equal(t, "mismatched", d.Compatibility.State)
}

func TestOwnedDecisionCloseAndCancellationNeverPass(t *testing.T) {
	closeFailure := errors.New("synthetic retained-root close failure")
	readFailure := errors.New("synthetic earlier read failure")
	for _, prior := range []error{nil, readFailure} {
		root, err := os.OpenRoot(t.TempDir())
		require.NoError(t, err)
		d := InitialDecision()
		d.Accepted = true
		err = prior
		finishOwnedDecision(t.Context(), &d, &err, func() error {
			return errors.Join(root.Close(), closeFailure)
		})
		require.False(t, d.Accepted)
		require.ErrorIs(t, err, closeFailure)
		if prior != nil {
			require.ErrorIs(t, err, prior)
		}
		_, rootErr := root.Stat(".")
		require.ErrorIs(t, rootErr, os.ErrClosed)
	}
	ctx, cancel := context.WithCancel(t.Context())
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	d := InitialDecision()
	d.Accepted = true
	finishOwnedDecision(ctx, &d, &err, func() error {
		closeErr := root.Close()
		cancel()
		return closeErr
	})
	require.False(t, d.Accepted)
	require.ErrorIs(t, err, context.Canceled)
}
