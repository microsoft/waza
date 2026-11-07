package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnerunit"
)

func run() (err error) {
	if len(os.Args) != 3 {
		return errors.New("provide the browser fixture store and native mock request paths")
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
	return json.NewEncoder(os.Stdout).Encode(struct {
		ManifestSHA256 string `json:"manifest_sha256"`
		EvidenceSHA256 string `json:"evidence_sha256"`
		ExecutionID    string `json:"execution_id"`
	}{source.ManifestDigest, published.EvidenceSHA256, source.ExecutionID})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
