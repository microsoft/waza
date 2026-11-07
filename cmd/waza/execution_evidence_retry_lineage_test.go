package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

var errRetryReadLimit = errors.New("retry fixture shared read limit exceeded")

type retryReadCounter struct {
	keys  []string
	limit int
}

func (c *retryReadCounter) get(ctx context.Context, backend artifactmanifest.ReadBackend, key string, limit int64) ([]byte, error) {
	if c.limit > 0 && len(c.keys) >= c.limit {
		return nil, errRetryReadLimit
	}
	c.keys = append(c.keys, key)
	return backend.Get(ctx, key, limit)
}

type retryNamespaceReads struct {
	artifactmanifest.ReadBackend
	counter *retryReadCounter
}

func (b retryNamespaceReads) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.counter.get(ctx, b.ReadBackend, key, limit)
}

type retryStoreReads struct {
	artifactmanifest.Backend
	counter *retryReadCounter
}

type retryNamespaceOverrides struct {
	artifactmanifest.ReadBackend
	overrides map[string][]byte
}

func (b retryNamespaceOverrides) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	data, err := b.ReadBackend.Get(ctx, key, limit)
	if err == nil {
		if override, exists := b.overrides[key]; exists {
			return bytes.Clone(override), nil
		}
	}
	return data, err
}

func (b retryStoreReads) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.counter.get(ctx, b.Backend, key, limit)
}

func retryFixtureReader(t *testing.T, backend artifactmanifest.Backend, counter *retryReadCounter) *executionledger.Reader {
	t.Helper()
	return retryFixtureReaderWithOverrides(t, backend, counter, nil)
}

func retryFixtureReaderWithOverrides(t *testing.T, backend artifactmanifest.Backend, counter *retryReadCounter, overrides map[string][]byte) *executionledger.Reader {
	t.Helper()
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	scoped, err := namespace.Reader(func(raw artifactmanifest.ReadBackend) artifactmanifest.ReadBackend {
		return retryNamespaceOverrides{ReadBackend: retryNamespaceReads{ReadBackend: raw, counter: counter}, overrides: overrides}
	})
	require.NoError(t, err)
	reader, err := executionledger.NewReader(scoped, retryStoreReads{Backend: backend, counter: counter})
	require.NoError(t, err)
	require.Empty(t, counter.keys, "reader construction must not perform provider reads")
	return reader
}

func retryFixtureCapture(t *testing.T, backend artifactmanifest.Backend, parent *executionattempt.Parent, completed bool) (executionattempt.Source, *evidencestate.Inspection) {
	t.Helper()
	prepared, err := evidencestate.CaptureReport(categoryDegradationReport(t), nil)
	require.NoError(t, err)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	// Use the original binding from the actual prepared capture, not a
	// reconstructed identity or an ordinal-derived execution identifier.
	artifacts, objects, err := prepared.Artifacts()
	require.NoError(t, err)
	require.NotEmpty(t, artifacts)
	index, err := evidencestate.ParseCapture(objects[evidencestate.CaptureArtifactName])
	require.NoError(t, err)
	creator, err := ledger.Reserve(t.Context(), index.Binding, parent)
	require.NoError(t, err)
	reservation, err := ledger.Reservation(creator)
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: reservation.Attempt.ExecutionID}
	if !completed {
		return source, nil
	}
	receipt, err := evidencestate.PersistOwnedCapture(t.Context(), backend, prepared, ledger, creator)
	require.NoError(t, err)
	require.NotNil(t, receipt.Document.ExecutionSource)
	return *receipt.Document.ExecutionSource, &receipt
}

func TestExecutionEvidenceRetryParentUsesActualCompletedSourceWithoutRewritingPublicLineage(t *testing.T) {
	fixture := newCategoryDegradationFixture(t, nil)
	parent := *fixture.receipt.Document.ExecutionSource
	child, receipt := retryFixtureCapture(t, fixture.backend, &executionattempt.Parent{
		ExecutionID: parent.ExecutionID, Relationship: "retry",
	}, true)
	require.NotEqual(t, parent.ExecutionID, child.ExecutionID)
	counter := &retryReadCounter{}
	reader := retryFixtureReader(t, fixture.backend, counter)
	state, link, err := evidencestate.InspectRetryParent(t.Context(), reader, child, "execution-attempt.json")
	require.NoError(t, err)
	require.Equal(t, evidencestate.Present, state)
	require.Equal(t, &evidencestate.Lineage{Manifest: parent.ManifestDigest, Relationship: evidencestate.Retry}, link)
	require.NotEqual(t, child.ManifestDigest, link.Manifest)
	require.Contains(t, counter.keys, "completion/"+parent.ExecutionID)
	require.Contains(t, counter.keys, "reservation/"+parent.ExecutionID)
	derived, err := evidencestate.Publish(t.Context(), retryStoreReads{Backend: fixture.backend, counter: counter}, *receipt.Document)
	require.NoError(t, err)
	require.Equal(t, receipt.EvidenceSHA256, derived.EvidenceSHA256)
	require.Empty(t, receipt.Document.Lineage, "qualified output must not mutate persisted metadata")
	output, err := runArtifactCLI(t, fixture.directory, "evidence", "categories", derived.EvidenceSHA256)
	require.NoError(t, err)
	response := fixture.get(t, derived.EvidenceSHA256, "")
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, string(output), response.Body.String())
	require.Contains(t, response.Body.String(), `"lineage":[]`)
	require.NotContains(t, response.Body.String(), parent.ManifestDigest)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	node := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { parseRecordedCategories } from "../../web/src/api/executionEvidenceCategories.ts";
const wire = readFileSync(0, "utf8");
const original = JSON.parse(wire);
const result = parseRecordedCategories(wire, original.evidence_sha256, 200);
assert.equal(result.sourceVerified, true);
assert.equal(result.categories.length, 6);
assert.equal(original.document.lineage.length, 0);
`)
	node.Stdin = bytes.NewReader(response.Body.Bytes())
	result, err := node.CombinedOutput()
	require.NoError(t, err, string(result))
	original, err := fixture.backend.Get(t.Context(), "manifest-"+parent.ManifestDigest, artifactmanifest.MaxManifestBytes)
	require.NoError(t, err)
	require.Equal(t, fixture.manifest, original)
	for _, suffix := range []string{"/document", "/csv"} {
		export := fixture.get(t, derived.EvidenceSHA256, suffix)
		require.Equal(t, http.StatusOK, export.Code)
		require.NotEmpty(t, export.Header().Get("Content-Disposition"))
	}
}

func TestExecutionEvidenceRetryParentMissingUnsupportedCorruptAndReadLimit(t *testing.T) {
	for _, control := range []string{"no-parent", "uncompleted-parent", "branch", "corrupt-parent", "read-limit", "descriptor-alias"} {
		t.Run(control, func(t *testing.T) {
			fixture := newCategoryDegradationFixture(t, nil)
			parent := *fixture.receipt.Document.ExecutionSource
			child := parent
			if control == "uncompleted-parent" {
				parent, _ = retryFixtureCapture(t, fixture.backend, nil, false)
			}

			if control != "no-parent" {
				relationship := "retry"
				if control == "branch" {
					relationship = "branch"
				}
				child, _ = retryFixtureCapture(t, fixture.backend, &executionattempt.Parent{
					ExecutionID: parent.ExecutionID, Relationship: relationship,
				}, true)
			}
			counter := &retryReadCounter{}
			reader := retryFixtureReader(t, fixture.backend, counter)
			name := "execution-attempt.json"
			switch control {
			case "corrupt-parent":
				ledger, err := executionledger.New(fixture.backend)
				require.NoError(t, err)
				completed, err := ledger.ReadCompleted(t.Context(), parent, name)
				require.NoError(t, err)
				data := bytes.Clone(completed.Publication.ArtifactBytes[name])
				require.NotEmpty(t, data)
				path := filepath.Join(fixture.storeRoot, "blob-"+artifactmanifest.Hash(data))
				data[0] ^= 1
				require.NoError(t, os.Chmod(path, 0o600))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			case "read-limit":
				baseline := &retryReadCounter{}
				baselineReader := retryFixtureReader(t, fixture.backend, baseline)
				state, link, err := evidencestate.InspectRetryParent(t.Context(), baselineReader, parent, name)
				require.NoError(t, err)
				require.Equal(t, evidencestate.Unavailable, state)
				require.Nil(t, link)
				counter.limit = len(baseline.keys)
				require.Positive(t, counter.limit)
			case "descriptor-alias":
				name = "terminal.json"
			}
			state, link, err := evidencestate.InspectRetryParent(t.Context(), reader, child, name)
			require.Nil(t, link)
			switch control {
			case "no-parent":
				require.NoError(t, err)
				require.Equal(t, evidencestate.Unavailable, state)
			case "uncompleted-parent":
				require.ErrorIs(t, err, artifactmanifest.ErrMissing)
				require.Equal(t, evidencestate.Unavailable, state)
			case "branch":
				require.ErrorIs(t, err, evidencestate.ErrInvalid)
				require.Equal(t, evidencestate.Unavailable, state)
				require.NotContains(t, strings.Join(counter.keys, "\n"), "completion/"+parent.ExecutionID)
			case "corrupt-parent":
				require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
				require.Equal(t, evidencestate.Unavailable, state)
			case "read-limit":
				require.ErrorIs(t, err, errRetryReadLimit)
				require.Equal(t, evidencestate.Unavailable, state)
				require.Len(t, counter.keys, counter.limit, "no raw-provider reads may bypass the shared limit")
			case "descriptor-alias":
				require.ErrorIs(t, err, artifactmanifest.ErrConflict)
				require.Equal(t, evidencestate.Conflicting, state)
			}
		})
	}
	for _, reader := range []*executionledger.Reader{nil, {}} {
		state, link, err := evidencestate.InspectRetryParent(t.Context(), reader, executionattempt.Source{}, "execution-attempt.json")
		require.ErrorIs(t, err, executionledger.ErrCapability)
		require.Equal(t, evidencestate.Unavailable, state)
		require.Nil(t, link)
	}
}

func TestExecutionEvidenceRetryParentPreservesFailedCanceledAndFinalizedReuse(t *testing.T) {
	for _, control := range []string{"grader-failure", "canceled"} {
		t.Run(control, func(t *testing.T) {
			fixture := newCategoryDegradationFixture(t, nil)
			raw, err := os.ReadFile("../../examples/runner/unit.json")
			require.NoError(t, err)
			request, err := runnercontract.Parse(raw)
			require.NoError(t, err)
			if control == "grader-failure" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- unmatched-parent-control", 1)
				require.Contains(t, request.Input.Task, "unmatched-parent-control")
			}
			wire, err := json.Marshal(request)
			require.NoError(t, err)
			submission, err := json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: wire,
			})
			require.NoError(t, err)
			hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
			require.NoError(t, err)
			service := runnerunit.NewService(nil)
			if control == "canceled" {
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, context.Canceled
				})
			}
			result, err := service.Submit(t.Context(), submission, fixture.backend, hooks)
			require.NoError(t, err)
			require.NotNil(t, result.Completion)
			require.NotNil(t, result.Reservation)
			require.NotZero(t, result.ExitCode)
			parent := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			ledger, err := executionledger.New(fixture.backend)
			require.NoError(t, err)
			original, err := ledger.ReadCompleted(t.Context(), parent, "terminal.json")
			require.NoError(t, err)
			report := runnerunit.NewService(nil).Run(t.Context(), wire)
			require.Equal(t, 1, report.Calls)
			prepared, err := evidencestate.CaptureReport(report, nil)
			require.NoError(t, err)
			_, binding := report.Terminal()
			require.NotNil(t, binding)
			creator, err := ledger.Reserve(t.Context(), artifactmanifest.Binding{
				Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
			}, &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"})
			require.NoError(t, err)
			child, err := evidencestate.PersistOwnedCapture(t.Context(), fixture.backend, prepared, ledger, creator)
			require.NoError(t, err)
			counter := &retryReadCounter{}
			reader := retryFixtureReader(t, fixture.backend, counter)
			state, link, err := evidencestate.InspectRetryParent(t.Context(), reader, *child.Document.ExecutionSource, child.Document.AttemptArtifact)
			require.NoError(t, err)
			require.Equal(t, evidencestate.Present, state)
			require.Equal(t, &evidencestate.Lineage{Manifest: parent.ManifestDigest, Relationship: evidencestate.Retry}, link)
			require.Empty(t, child.Document.Lineage)
			request.Selection.Attempt++
			wire, err = json.Marshal(request)
			require.NoError(t, err)
			submission, err = json.Marshal(runnerunit.SubmissionRequest{
				Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: wire,
				Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &parent},
			})
			require.NoError(t, err)
			reused, err := service.Submit(t.Context(), submission, fixture.backend, hooks)
			require.NoError(t, err)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, result.ExitCode, reused.ExitCode)
			require.Equal(t, result.Terminal, reused.Terminal)
			require.Equal(t, parent.ExecutionID, reused.Reservation.ExecutionID)
			require.Equal(t, parent.ManifestDigest, reused.Completion.ManifestDigest)
			after, err := ledger.ReadCompleted(t.Context(), parent, "terminal.json")
			require.NoError(t, err)
			require.Equal(t, original.Publication.ManifestBytes, after.Publication.ManifestBytes)
			require.Equal(t, original.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
			state, link, err = evidencestate.InspectRetryParent(t.Context(), reader, parent, "execution-attempt.json")
			require.NoError(t, err)
			require.Equal(t, evidencestate.Unavailable, state)
			require.Nil(t, link, "same-source restore must not invent a child or resume edge")
		})
	}
}

func TestExecutionEvidenceRetryParentRejectsKnownCycleAndDoesNotWalkGrandparent(t *testing.T) {
	fixture := newCategoryDegradationFixture(t, nil)
	grandparent := *fixture.receipt.Document.ExecutionSource
	parent, _ := retryFixtureCapture(t, fixture.backend, &executionattempt.Parent{
		ExecutionID: grandparent.ExecutionID, Relationship: "retry",
	}, true)
	child, _ := retryFixtureCapture(t, fixture.backend, &executionattempt.Parent{
		ExecutionID: parent.ExecutionID, Relationship: "retry",
	}, true)
	counter := &retryReadCounter{}
	reader := retryFixtureReader(t, fixture.backend, counter)
	state, link, err := evidencestate.InspectRetryParent(t.Context(), reader, child, "execution-attempt.json")
	require.NoError(t, err)
	require.Equal(t, evidencestate.Present, state)
	require.Equal(t, &evidencestate.Lineage{Manifest: parent.ManifestDigest, Relationship: evidencestate.Retry}, link)
	require.NotContains(t, strings.Join(counter.keys, "\n"), grandparent.ExecutionID, "one immediate edge must not become an unbounded history walk")

	ledger, err := executionledger.New(fixture.backend)
	require.NoError(t, err)
	original, err := ledger.ReadCompleted(t.Context(), parent, "execution-attempt.json")
	require.NoError(t, err)
	attempt, err := executionattempt.ParseAttempt(original.Reservation.Bytes)
	require.NoError(t, err)
	attempt.Parent = &executionattempt.Parent{ExecutionID: child.ExecutionID, Relationship: "retry"}
	altered, err := executionattempt.EncodeAttempt(*attempt)
	require.NoError(t, err)
	store := artifactmanifest.NewStore(fixture.backend)
	manifest := original.Publication.Manifest
	found := false
	for i := range manifest.Artifacts {
		artifact := &manifest.Artifacts[i]
		if artifact.Name == "execution-attempt.json" {
			found = true
			artifact.SHA256, artifact.SizeBytes = artifactmanifest.Hash(altered), int64(len(altered))
			require.NoError(t, store.Put(t.Context(), *artifact, altered))
		}
	}
	require.True(t, found)
	wire, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	digest, err := store.Create(t.Context(), wire)
	require.NoError(t, err)
	_, err = store.Finalize(t.Context(), digest)
	require.NoError(t, err)
	completion := original.Completion
	completion.ManifestDigest, completion.ReservationDigest = digest, artifactmanifest.Hash(altered)
	completionBytes, err := executionledger.EncodeCompletion(completion)
	require.NoError(t, err)
	// Coherent authored replacement controls retain real finalized Store bytes
	// but replace the namespace read view. They do not rewrite owner records.
	overrides := map[string][]byte{
		"reservation/" + parent.ExecutionID: altered,
		"completion/" + parent.ExecutionID:  completionBytes,
	}
	counter = &retryReadCounter{}
	reader = retryFixtureReaderWithOverrides(t, fixture.backend, counter, overrides)
	state, link, err = evidencestate.InspectRetryParent(t.Context(), reader, child, "execution-attempt.json")
	require.ErrorIs(t, err, artifactmanifest.ErrConflict)
	require.Equal(t, evidencestate.Conflicting, state)
	require.Nil(t, link)
	after, err := ledger.ReadCompleted(t.Context(), parent, "execution-attempt.json")
	require.NoError(t, err)
	require.Equal(t, original.Reservation.Bytes, after.Reservation.Bytes)
	require.Equal(t, original.CompletionBytes, after.CompletionBytes)
	require.Equal(t, original.Publication.ManifestBytes, after.Publication.ManifestBytes)
}
