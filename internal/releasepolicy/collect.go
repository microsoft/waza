package releasepolicy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/microsoft/waza/internal/models"
)

// Collector supplies instrumented observations, not arbitrary legacy summaries.
// Initialize may start engines only after both BEGIN receipts and directory
// entries have been synced. Attempt is called only after its durable start.
type Collector struct {
	Initialize    func(context.Context) error
	Attempt       func(context.Context, AttemptKey) (AttemptObservation, error)
	Usage         func(context.Context, Arm) ([]UsageAxis, error)
	Shutdown      func(context.Context) error
	BeforePublish func(context.Context) error
}

type AttemptObservation struct {
	Summary AttemptSummary
	Result  ActualRunRow
	Output  *AssuranceOutput
}

// Collect creates a new private collection, never adopts or resumes one. File
// and containing-directory fsync failures abort the new protocol. These
// primitives provide process-exit persistence; machine-crash durability still
// depends on the filesystem/storage honoring them. Legacy writers are untouched.
func Collect(ctx context.Context, policyData []byte, directory string, collector Collector) (err error) {
	return collect(ctx, policyData, directory, collector, nil)
}

func collect(ctx context.Context, policyData []byte, directory string, collector Collector, assured *assuranceCollection) (err error) {
	p, err := DecodePolicy(policyData)
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return fmt.Errorf("private durable release collection is unsupported on %s; legacy evaluation remains available", runtime.GOOS)
	}
	if collector.Initialize == nil || collector.Attempt == nil || collector.Usage == nil || collector.Shutdown == nil {
		return fmt.Errorf("collection requires safe engine, attempt, final usage and shutdown instrumentation")
	}
	collectionID, err := newIdentity()
	if err != nil {
		return err
	}
	if assured != nil {
		collectionID = assured.contract.Digest.SHA256
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("reserving new collection directory (no resume): %w", err)
	}
	if err := syncDirectory(filepath.Dir(directory)); err != nil {
		return err
	}
	if err := writeExclusive(directory, "policy.json", policyData); err != nil {
		return err
	}
	if assured != nil {
		if err := assured.open(directory); err != nil {
			return err
		}
		defer func() { err = errors.Join(err, assured.close()) }()
	}
	stream, err := os.OpenFile(filepath.Join(directory, "journal.ndjson"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating ordered event stream: %w", err)
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	if err := stream.Sync(); err != nil {
		return fmt.Errorf("syncing empty event stream: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	journal := Journal{Kind: JournalKind, Version: Version, CollectionID: collectionID, Events: []Event{}}
	streamHash := sha256.New()
	appendEvent := func(event Event) error {
		event.Sequence = len(journal.Events) + 1
		event.CollectionID, event.PolicyDigest = collectionID, p.Digest
		data, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("encoding event %d: %w", event.Sequence, err)
		}
		data = append(data, '\n')
		if _, err := stream.Write(data); err != nil {
			return fmt.Errorf("persisting event %d: %w", event.Sequence, err)
		}
		if err := stream.Sync(); err != nil {
			return fmt.Errorf("syncing event %d: %w", event.Sequence, err)
		}
		if _, err := streamHash.Write(data); err != nil {
			return fmt.Errorf("hashing persisted event bytes: %w", err)
		}
		journal.Events = append(journal.Events, event)
		return nil
	}
	evals := map[Arm]string{}
	results := map[Arm]Results{}
	for _, arm := range []Arm{Baseline, Candidate} {
		eval, err := newIdentity()
		if err != nil {
			return err
		}
		evals[arm] = eval
		results[arm] = Results{Kind: "waza.release-results", Version: Version,
			CollectionID: collectionID, Arm: arm, EvalID: eval, Rows: []ActualRunRow{}}
		begin := Receipt{Kind: ReceiptKind, Version: Version, State: "begin",
			CollectionID: collectionID, PolicyDigest: p.Digest, Arm: arm,
			EvalID: eval, PlanDigest: p.Arms[arm].Digest, Samples: PlannedSamples(p)}
		data, err := SealJSON(begin)
		if err != nil {
			return err
		}
		if err := writeExclusive(directory, string(arm)+".begin.json", data); err != nil {
			return err
		}
		if err := appendEvent(Event{Type: "begin_arm", Arm: arm, EvalID: eval}); err != nil {
			return err
		}
	}
	defer func() { err = errors.Join(err, collector.Shutdown(context.WithoutCancel(ctx))) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := collector.Initialize(ctx); err != nil {
		return fmt.Errorf("initializing paired collection: %w", err)
	}
	for ci, cluster := range p.Design.Clusters {
		if err := appendEvent(Event{Type: "cluster_start", ClusterID: cluster.ID}); err != nil {
			return err
		}
		for _, arm := range p.Design.Allocation.Assignments[ci].Order {
			for _, task := range cluster.Tasks {
				for _, trial := range task.TrialOrdinals {
					sample := SampleKey{ClusterID: cluster.ID, TaskID: task.ID, Trial: trial}
					attempts := []AttemptSummary{}
					for ordinal := 1; ordinal <= settingsFor(p, arm, task.ID).MaxAttempts; ordinal++ {
						if err := ctx.Err(); err != nil {
							return err
						}
						key := AttemptKey{SampleKey: sample, Arm: arm, EvalID: evals[arm], Attempt: ordinal}
						if err := appendEvent(Event{Type: "attempt_start", Key: &key}); err != nil {
							return err
						}
						observation, err := collector.Attempt(ctx, key)
						if err != nil {
							return fmt.Errorf("observing %s/%s trial %d attempt %d: %w", arm, task.ID, trial, ordinal, err)
						}
						attempt := observation.Summary
						if attempt.Key != key {
							return fmt.Errorf("collector returned a different allocated attempt identity")
						}
						if err := validateAttempt(p, attempt); err != nil {
							return fmt.Errorf("invalid observed attempt: %w", err)
						}
						if err := VerifyRunRow(attempt, observation.Result); err != nil {
							return fmt.Errorf("observed raw result contradicts attempt summary: %w", err)
						}
						if assured != nil {
							if err := assured.append(observation); err != nil {
								return fmt.Errorf("persisting full assurance attempt before core terminal: %w", err)
							}
						}
						if err := appendEvent(Event{Type: "attempt_terminal", Attempt: &attempt}); err != nil {
							return err
						}
						attempts = append(attempts, attempt)
						rows := results[arm]
						rows.Rows = append(rows.Rows, observation.Result)
						results[arm] = rows
						if attempt.Status == "passed" || attempt.Category != "behavioral" {
							break
						}
					}
					terminal := summarizeTrial(sample, attempts)
					if err := appendEvent(Event{Type: "trial_terminal", Arm: arm, Trial: &terminal}); err != nil {
						return err
					}
				}
			}
		}
	}
	// Finalize engines before collecting final billing. A shutdown fallback may
	// obtain credits, but missing or partial totals are never replaced with zero.
	if err := collector.Shutdown(ctx); err != nil {
		return fmt.Errorf("finalizing collection engines: %w", err)
	}
	collector.Shutdown = func(context.Context) error { return nil }
	if assured != nil {
		if err := assured.close(); err != nil {
			return fmt.Errorf("closing full assurance attempt tape before publication: %w", err)
		}
	}
	if collector.BeforePublish != nil {
		if err := collector.BeforePublish(ctx); err != nil {
			return fmt.Errorf("rechecking prepublication sources: %w", err)
		}
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		usage, err := collector.Usage(ctx, arm)
		if err != nil {
			return fmt.Errorf("observing final %s billing: %w", arm, err)
		}
		if err := appendEvent(Event{Type: "end_arm", Arm: arm, Usage: usage}); err != nil {
			return err
		}
	}
	if err := appendEvent(Event{Type: "collection_end"}); err != nil {
		return err
	}
	journal.StreamDigest = models.EvidenceDigest{SHA256: hex.EncodeToString(streamHash.Sum(nil)), Encoding: "source-bytes"}
	data, err := SealJSON(journal)
	if err != nil {
		return err
	}
	sealed, err := DecodeJournal(data, p)
	if err != nil {
		return err
	}
	receipts, err := Replay(p, sealed)
	if err != nil {
		return err
	}
	if err := writeExclusive(directory, "journal.json", data); err != nil {
		return err
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		r := receipts[arm]
		result, err := json.Marshal(results[arm])
		if err != nil {
			return err
		}
		if err := writeExclusive(directory, string(arm)+".results.json", result); err != nil {
			return err
		}
		binding, err := json.Marshal(r.Binding)
		if err != nil {
			return err
		}
		if err := writeExclusive(directory, string(arm)+".result-binding.json", binding); err != nil {
			return err
		}
		data, err := SealJSON(r)
		if err != nil {
			return err
		}
		if err := writeExclusive(directory, string(arm)+".final.json", data); err != nil {
			return err
		}
	}
	if assured != nil {
		if err := assured.finish(directory, p, sealed); err != nil {
			return err
		}
	}
	return nil
}

func newIdentity() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("allocating collection identity: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func syncDirectory(directory string) (err error) {
	d, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("opening containing directory for durability: %w", err)
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("containing-directory durability unavailable; collection cannot start: %w", err)
	}
	return nil
}

func writeExclusive(directory, name string, data []byte) (err error) {
	f, err := os.OpenFile(filepath.Join(directory, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating collection artifact %s: %w", name, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing collection artifact %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing collection artifact %s: %w", name, err)
	}
	return syncDirectory(directory)
}
