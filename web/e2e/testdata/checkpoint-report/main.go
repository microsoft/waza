package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

func run() (resultErr error) {
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		_, err = runnercheckpoint.ParseResult(data)
		return err
	}
	if len(os.Args) != 2 {
		return fmt.Errorf("expected storage directory")
	}
	request, err := os.ReadFile("examples/runner/unit.json")
	if err != nil {
		return err
	}
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: request,
	})
	if err != nil {
		return err
	}
	store, err := artifactmanifest.OpenLocal(os.Args[1])
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("closing fixture store: %w", err))
		}
	}()
	service, err := runnercheckpoint.NewService(runnerunit.NewService(nil), strings.Repeat("c", 40))
	if err != nil {
		return err
	}
	var report json.RawMessage
	hooks := runnercheckpoint.Hooks{Submission: runnerunit.SubmissionHooks{
		Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, snapshot runnerunit.SubmissionSnapshot) ([]runnerunit.SubmissionArtifact, error) {
			report = snapshot.Report
			return nil, nil
		},
	}}
	completed, err := service.Submit(context.Background(), wire, store, hooks)
	if err != nil {
		return err
	}
	var next runnercontract.Request
	if err := json.Unmarshal(request, &next); err != nil {
		return err
	}
	next.Selection.Attempt++
	nextRequest, err := json.Marshal(next)
	if err != nil {
		return err
	}
	wire, err = json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: nextRequest,
	})
	if err != nil {
		return err
	}
	reused, err := service.Resume(context.Background(), wire, store, completed.FinalizedDigest, runnercheckpoint.Hooks{})
	if err != nil {
		return err
	}
	// Current public capture withholds reports; exercise the optional legacy
	// report field using the original producer snapshot, not reconstructed data.
	completed.Submission.Report = report
	reused.Submission.Report = report
	for _, result := range []runnercheckpoint.Result{completed, reused} {
		data, err := runnercheckpoint.EncodeResult(result)
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
