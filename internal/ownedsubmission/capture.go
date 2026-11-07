package ownedsubmission

import (
	"bytes"
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

// CaptureHooks composes the actual category owner's conservative policy with
// invocation observers. It does not approve unrestricted payload disclosure.
func CaptureHooks(next runnerunit.SubmissionHooks) (runnerunit.SubmissionHooks, error) {
	if next.PreparePublication != nil || next.ValidatePublication != nil || next.Artifacts != nil {
		return runnerunit.SubmissionHooks{}, fmt.Errorf("owned submission requires one publication policy")
	}
	hooks := next
	hooks.Admission = func(ctx context.Context, binding artifactmanifest.Binding) error {
		if err := evidencestate.ValidateCaptureBinding(binding); err != nil {
			return err
		}
		if next.Admission != nil {
			return next.Admission(ctx, binding)
		}
		return nil
	}
	hooks.ReuseAdmission = func(ctx context.Context, source *executionledger.CompletedExecution) error {
		if err := validateSourceDisclosure(source); err != nil {
			return err
		}
		if next.ReuseAdmission != nil {
			return next.ReuseAdmission(ctx, source)
		}
		return nil
	}
	hooks.PreparePublication = func(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator,
		source runnerunit.SubmissionSnapshot) (runnerunit.SubmissionPublication, error) {
		if err := ledger.ValidateCreator(ctx, creator); err != nil {
			return runnerunit.SubmissionPublication{}, err
		}
		retained, err := ledger.Reservation(creator)
		if err != nil {
			return runnerunit.SubmissionPublication{}, err
		}
		canonical, err := executionattempt.EncodeAttempt(source.Reservation.Attempt)
		if err != nil {
			return runnerunit.SubmissionPublication{}, err
		}
		if !bytes.Equal(retained.Bytes, source.Reservation.Bytes) || !bytes.Equal(retained.Bytes, canonical) ||
			retained.Digest != source.Reservation.Digest {
			return runnerunit.SubmissionPublication{}, executionledger.ErrContext
		}
		return prepareCapturePublication(source)
	}
	hooks.ValidatePublication = func(_ context.Context, source runnerunit.SubmissionSnapshot, publication runnerunit.SubmissionPublication) error {
		expected, err := prepareCapturePublication(source)
		if err != nil {
			return err
		}
		expectedTerminal, err := runnerterminal.Encode(expected.Terminal, source.Reservation.Attempt.Binding.TerminalBinding())
		if err != nil {
			return err
		}
		terminal, err := runnerterminal.Encode(publication.Terminal, source.Reservation.Attempt.Binding.TerminalBinding())
		if err != nil {
			return fmt.Errorf("encoding prepared terminal: %w", err)
		}
		if !bytes.Equal(terminal, expectedTerminal) {
			return fmt.Errorf("prepared terminal contradicts independently retained original capture")
		}
		if len(expected.Artifacts) != len(publication.Artifacts) {
			return fmt.Errorf("prepared category artifact inventory contradicts original capture")
		}
		objects := make(map[string]runnerunit.SubmissionArtifact, len(expected.Artifacts))
		for _, artifact := range expected.Artifacts {
			objects[artifact.Name] = artifact
		}
		for _, artifact := range publication.Artifacts {
			expected, found := objects[artifact.Name]
			if !found || artifact.MediaType != expected.MediaType || !bytes.Equal(artifact.Data, expected.Data) {
				return fmt.Errorf("prepared category content contradicts independently retained original observation")
			}
			delete(objects, artifact.Name)
		}
		return nil
	}
	return hooks, nil
}

func prepareCapturePublication(source runnerunit.SubmissionSnapshot) (runnerunit.SubmissionPublication, error) {
	prepared, err := evidencestate.CaptureReport(source.ObservedReport, []evidencestate.Category{
		evidencestate.Conversation, evidencestate.Tool, evidencestate.Reliability,
	})
	if err != nil {
		return runnerunit.SubmissionPublication{}, err
	}
	descriptors, objects, err := prepared.Artifacts()
	if err != nil {
		return runnerunit.SubmissionPublication{}, err
	}
	terminal, err := runnerterminal.Validate(objects["terminal.json"], source.Reservation.Attempt.Binding.TerminalBinding())
	if err != nil {
		return runnerunit.SubmissionPublication{}, err
	}
	result := runnerunit.SubmissionPublication{Terminal: *terminal, Artifacts: []runnerunit.SubmissionArtifact{}}
	for _, descriptor := range descriptors {
		data := objects[descriptor.Name]
		if descriptor.SHA256 != artifactmanifest.Hash(data) || descriptor.SizeBytes != int64(len(data)) {
			return runnerunit.SubmissionPublication{}, artifactmanifest.ErrCorrupt
		}
		result.Artifacts = append(result.Artifacts, runnerunit.SubmissionArtifact{
			Name: descriptor.Name, MediaType: descriptor.MediaType, Data: data,
		})
	}
	attempt, err := executionattempt.EncodeAttempt(source.Reservation.Attempt)
	if err != nil {
		return runnerunit.SubmissionPublication{}, err
	}
	if !bytes.Equal(attempt, source.Reservation.Bytes) || artifactmanifest.Hash(attempt) != source.Reservation.Digest {
		return runnerunit.SubmissionPublication{}, executionledger.ErrContext
	}
	result.Artifacts = append(result.Artifacts, runnerunit.SubmissionArtifact{
		Name: "execution-attempt.json", MediaType: "application/json", Data: bytes.Clone(attempt),
	})
	return result, nil
}

func validateSourceDisclosure(source *executionledger.CompletedExecution) error {
	if source == nil {
		return fmt.Errorf("owned source disclosure requires verified original context")
	}
	binding := source.Reservation.Attempt.Binding
	if err := evidencestate.ValidateCaptureBinding(binding); err != nil {
		return err
	}
	terminal, err := runnerterminal.Validate(source.Publication.ArtifactBytes[source.Publication.Manifest.TerminalArtifact], binding.TerminalBinding())
	if err != nil {
		return err
	}
	for _, diagnostic := range terminal.Diagnostics {
		if diagnostic.Message != "" && diagnostic.Message != "[REDACTED]" {
			return fmt.Errorf("original source diagnostics are restricted; source was not rewritten or reused")
		}
	}
	if terminal.Observation != nil && terminal.Observation.ErrorMsg != "" && terminal.Observation.ErrorMsg != "[REDACTED]" {
		return fmt.Errorf("original source observation text is restricted; source was not rewritten or reused")
	}
	return nil
}
