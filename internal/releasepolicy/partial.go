package releasepolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
)

func accountIncomplete(ctx context.Context, directory string, p *Policy, d *Decision) error {
	for _, arm := range []Arm{Baseline, Candidate} {
		d.Accounting[arm] = Reliability{PlannedTrials: len(PlannedSamples(p))}
	}
	stream, err := readArtifactContext(ctx, directory, "journal.ndjson")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading incomplete durable tape: %w", err)
	}
	j := Journal{Kind: JournalKind, Version: Version, Digest: p.Digest, Events: []Event{}}
	lines := bytes.Split(stream, []byte{'\n'})
	// An unterminated final fragment was not a complete persisted transition.
	// Keep the publication incomplete rather than repairing or adopting it.
	for _, line := range lines[:len(lines)-1] {
		if err := ctx.Err(); err != nil {
			return err
		}
		object, err := objectJSON(line)
		if err != nil {
			return err
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil {
			return err
		}
		if err := requireFields(object, reflect.TypeFor[Event](), ""); err != nil {
			return err
		}
		j.Events = append(j.Events, event)
	}
	if len(j.Events) == 0 {
		return nil
	}
	j.CollectionID = j.Events[0].CollectionID
	return accountPrefix(p, &j, d)
}

func accountPrefix(p *Policy, j *Journal, d *Decision) error {
	if _, err := replay(p, j, true); err != nil {
		return fmt.Errorf("invalid incomplete event prefix: %w", err)
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		d.Accounting[arm] = Reliability{PlannedTrials: len(PlannedSamples(p))}
	}
	// Counts are explicitly tape-only: without final raw-result cross-checks,
	// endpoint rates/assurance/statistical acceptance remain unavailable.
	started := map[Arm]map[SampleKey]bool{Baseline: {}, Candidate: {}}
	for _, event := range j.Events {
		switch event.Type {
		case "attempt_start":
			counts := d.Accounting[event.Key.Arm]
			counts.Attempts++
			counts.StartedAttempts++
			if !started[event.Key.Arm][event.Key.SampleKey] {
				counts.StartedTrials++
				started[event.Key.Arm][event.Key.SampleKey] = true
			}
			d.Accounting[event.Key.Arm] = counts
		case "attempt_terminal":
			counts := d.Accounting[event.Attempt.Key.Arm]
			counts.CompleteAttempts++
			d.Accounting[event.Attempt.Key.Arm] = counts
		}
	}
	d.Completeness.State = "partial"
	d.Completeness.Reasons = append(d.Completeness.Reasons,
		"Started/completed attempt counts are durable-prefix observations only; final raw results and endpoint rates are unavailable.")
	return nil
}
