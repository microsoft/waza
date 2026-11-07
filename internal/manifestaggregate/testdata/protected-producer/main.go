package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
)

func run() (err error) {
	if len(os.Args) != 3 && len(os.Args) != 4 {
		return errors.New("provide the browser fixture store and native mock request paths")
	}
	mode := ""
	if len(os.Args) == 4 {
		mode = os.Args[3]
		if mode != "retry" && mode != "missing-parent" {
			return errors.New("optional browser parent control must be retry or missing-parent")
		}
	}
	backend, err := artifactmanifest.OpenLocal(os.Args[1])
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, backend.Close()) }()
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
	})
	if err != nil {
		return err
	}
	hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
	if err != nil {
		return err
	}
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := service.Submit(ctx, wire, backend, hooks)
	if err != nil {
		return err
	}
	if result.Completion == nil || result.Reservation == nil || result.Terminal == nil ||
		result.EngineCalls != 1 || result.ExitCode != 0 {
		return errors.New("protected mock Submit did not produce a complete successful original")
	}
	source := executionattempt.Source{
		ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest,
	}
	var parent *executionattempt.Source
	if mode != "" {
		original := source
		parent = &original
		request, err := runnercontract.Parse(data)
		if err != nil {
			return err
		}
		request.Selection.Attempt++
		requestWire, err := json.Marshal(request)
		if err != nil {
			return err
		}
		wire, err := json.Marshal(runnerunit.SubmissionRequest{
			Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: requestWire,
			Policy: &runnerunit.SubmissionPolicy{Mode: "distinct",
				Parent: &executionattempt.Parent{ExecutionID: parent.ExecutionID, Relationship: "retry"}},
		})
		if err != nil {
			return err
		}
		result, err = service.Submit(ctx, wire, backend, hooks)
		if err != nil {
			return err
		}
		if result.Completion == nil || result.Reservation == nil || result.Terminal == nil ||
			result.EngineCalls != 1 || result.ExitCode != 0 || result.Reservation.ExecutionID == parent.ExecutionID {
			return errors.New("protected retry did not produce a distinct complete successful child")
		}
		source = executionattempt.Source{
			ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest,
		}
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		return err
	}
	completed, err := ledger.ReadCompleted(ctx, source, evidencestate.CaptureArtifactName)
	if err != nil {
		return err
	}
	index, err := evidencestate.ParseCapture(completed.Publication.ArtifactBytes[evidencestate.CaptureArtifactName])
	if err != nil {
		return err
	}
	document := evidencestate.Document{
		Kind: "waza.execution-evidence", SchemaVersion: evidencestate.Version, Publication: "derived",
		OriginManifest: source.ManifestDigest, ExecutionSource: &source, AttemptArtifact: "execution-attempt.json",
		Categories: index.Categories, Lineage: []evidencestate.Lineage{},
	}
	published, err := evidencestate.Publish(ctx, backend, document)
	if err != nil {
		return err
	}
	if mode == "missing-parent" {
		path := filepath.Join(os.Args[1], artifactmanifest.ExecutionLedgerNamespaceV1+"-completion-"+parent.ExecutionID)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing fixture parent completion: %w", err)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		ManifestSHA256 string                   `json:"manifest_sha256"`
		EvidenceSHA256 string                   `json:"evidence_sha256"`
		ExecutionID    string                   `json:"execution_id"`
		Parent         *executionattempt.Source `json:"parent,omitempty"`
	}{source.ManifestDigest, published.EvidenceSHA256, source.ExecutionID, parent})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
