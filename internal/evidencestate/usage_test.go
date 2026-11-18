package evidencestate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/models"
)

func usagePublication(t *testing.T, content []byte, state State, terminalReference bool) (artifactmanifest.Backend, string) {
	t.Helper()
	backend, _ := localStore(t)
	d, _, artifacts, origin := sourceFixture(t)
	name := "caller-assigned-usage-alias"
	if terminalReference {
		name = origin.TerminalArtifact
	} else {
		artifacts[name] = content
		origin.Artifacts = append(origin.Artifacts, artifactmanifest.Artifact{
			Name: name, SHA256: artifactmanifest.Hash(content), SizeBytes: int64(len(content)), MediaType: "application/json",
		})
	}
	for i := range d.Categories {
		if d.Categories[i].Category != Usage {
			continue
		}
		d.Categories[i].State, d.Categories[i].Source, d.Categories[i].Context = state, Authored, Declared
		d.Categories[i].Artifacts = []string{name}
		d.Categories[i].ObservedCount = intPointer(1)
		if state == Redacted || state == Unavailable {
			d.Categories[i].Artifacts = []string{}
			d.Categories[i].ObservedCount = nil
		}
		switch state {
		case Redacted:
			d.Categories[i].Reason = Privacy
		case Unavailable:
			d.Categories[i].Reason = NotRecorded
		case Incomplete:
			d.Categories[i].Reason = PartialCapture
		case Conflicting:
			d.Categories[i].Reason = SourceConflict
		}
	}
	d.OriginManifest = publishOrigin(t, backend, origin, artifacts)
	receipt, err := Publish(context.Background(), backend, d)
	if err != nil {
		t.Fatal(err)
	}
	return backend, receipt.EvidenceSHA256
}

func completeRecordedUsage() models.UsageStats {
	zero := 0.0
	return models.UsageStats{
		AICredits: &zero,
		ModelMetrics: map[string]models.ModelUsage{
			"recorded-model": {AICredits: &zero},
		},
	}
}

func TestReadUsageAdmitsActualFieldsAndRecordedZero(t *testing.T) {
	for _, credits := range []float64{0, 0.12345678901234567} {
		usage := completeRecordedUsage()
		usage.AICredits = &credits
		backend, digest := usagePublication(t, wire(t, usage), Present, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if err != nil || observed.State != Present || observed.Usage == nil ||
			observed.Usage.AICredits == nil || *observed.Usage.AICredits != credits {
			t.Fatalf("recorded usage presence/precision lost: %+v, %v", observed, err)
		}
		if observed.Verification != "source-content-and-recorded-usage-shape-only" {
			t.Fatal("usage shape confused with execution attestation")
		}
		total := models.AggregateUsageStats([]*models.UsageStats{observed.Usage})
		if total == nil || total.AICredits == nil || *total.AICredits != credits {
			t.Fatal("existing complete-credit aggregator did not preserve recorded value")
		}
	}
}

func TestReadUsageIncompleteCreditsAndAttributionRemainIncomplete(t *testing.T) {
	for _, mutate := range []func(*models.UsageStats){
		func(u *models.UsageStats) { u.AICredits = nil },
		func(u *models.UsageStats) { u.ModelMetrics = nil },
		func(u *models.UsageStats) { u.ModelMetrics["recorded-model"] = models.ModelUsage{} },
	} {
		usage := completeRecordedUsage()
		mutate(&usage)
		backend, digest := usagePublication(t, wire(t, usage), Present, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if err != nil || observed.State != Incomplete || observed.DeclaredState != Present ||
			observed.Usage == nil || observed.Reason != "incomplete_credit_or_model_attribution" {
			t.Fatalf("declared present became authoritative incomplete billing: %+v, %v", observed, err)
		}
	}
}

func TestReadUsageRejectsMissingMalformedAndTerminalPayloads(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"turns":0,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":false}`),
		[]byte(`{"turns":0,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":0,"ai_credits":"0"}`),
		[]byte(`{"turns":0,"turns":1,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":0}`),
		[]byte(`{"turns":-1,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":0}`),
		[]byte(`{"turns":null,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":0}`),
		[]byte(`{"turns":0,"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"premium_requests":0,"ai_credits":1e999}`),
	} {
		backend, digest := usagePublication(t, data, Present, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if !errors.Is(err, ErrInvalid) || observed.State != Unavailable || observed.Usage != nil {
			t.Fatalf("digest-valid invalid usage became billing: %+v, %v", observed, err)
		}
	}

	backend, digest := usagePublication(t, nil, Present, true)
	observed, err := ReadUsage(context.Background(), backend, digest)
	if !errors.Is(err, ErrInvalid) || observed.Usage != nil {
		t.Fatal("terminal evidence was treated as usage")
	}
	backend, digest = usagePublication(t, []byte(`{"restricted":"not usage"}`), Redacted, false)
	observed, err = ReadUsage(context.Background(), backend, digest)
	if err != nil || observed.State != Redacted || observed.Usage != nil || observed.Verification != "declared-state-only" {
		t.Fatal("redacted category was parsed or reconstituted as usage")
	}
}

func TestReadUsageRejectsUnsafeProviderMetadataAndEmptyAttribution(t *testing.T) {
	for _, host := range []string{"user:credential@example.test", "example.test/private", "example.test?credential=value", "example.test#restricted"} {
		usage := completeRecordedUsage()
		usage.ProviderHost = host
		backend, digest := usagePublication(t, wire(t, usage), Present, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if !errors.Is(err, ErrInvalid) || observed.Usage != nil || observed.State != Unavailable {
			t.Fatal("provider credentials/path metadata became admitted usage")
		}
	}
	usage := completeRecordedUsage()
	usage.ModelMetrics[" "] = usage.ModelMetrics["recorded-model"]
	delete(usage.ModelMetrics, "recorded-model")
	backend, digest := usagePublication(t, wire(t, usage), Present, false)
	observed, err := ReadUsage(context.Background(), backend, digest)
	if !errors.Is(err, ErrInvalid) || observed.Usage != nil {
		t.Fatal("empty attribution became a known model")
	}
}

func TestReadUsageNonPresentAndPresentEmptyDoNotManufactureZeroUsage(t *testing.T) {
	for _, state := range []State{Unavailable, Incomplete, Redacted, Conflicting} {
		backend, digest := usagePublication(t, wire(t, completeRecordedUsage()), state, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if err != nil || observed.State != state || observed.DeclaredState != state || observed.Usage != nil {
			t.Fatalf("non-present declaration became usable billing: %+v, %v", observed, err)
		}
	}
	backend, _ := localStore(t)
	d, _, artifacts, origin := sourceFixture(t)
	for i := range d.Categories {
		if d.Categories[i].Category == Usage {
			d.Categories[i].State = Present
			d.Categories[i].Artifacts = []string{}
			d.Categories[i].ObservedCount = intPointer(0)
		}
	}
	d.OriginManifest = publishOrigin(t, backend, origin, artifacts)
	receipt, err := Publish(context.Background(), backend, d)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ReadUsage(context.Background(), backend, receipt.EvidenceSHA256)
	if err != nil || observed.State != Present || observed.Usage != nil || observed.Reason != "declared_present_empty" {
		t.Fatal("observed-empty declaration invented zero-valued usage")
	}
}

func TestReadUsageNullCreditsAndAlternativeValidHostMetadata(t *testing.T) {
	usage := completeRecordedUsage()
	raw := strings.Replace(string(wire(t, usage)), `"ai_credits":0`, `"ai_credits":null`, 1)
	backend, digest := usagePublication(t, []byte(raw), Present, false)
	observed, err := ReadUsage(context.Background(), backend, digest)
	if err != nil || observed.State != Incomplete || observed.Usage == nil || observed.Usage.AICredits != nil {
		t.Fatal("explicit unknown credits became recorded zero")
	}
	for _, host := range []string{"example.test", "localhost:8080", "[::1]:8080"} {
		usage.ProviderHost = host
		backend, digest := usagePublication(t, wire(t, usage), Present, false)
		observed, err := ReadUsage(context.Background(), backend, digest)
		if err != nil || observed.State != Present || observed.Usage == nil || observed.Usage.ProviderHost != host {
			t.Fatalf("valid native host-only metadata was changed: %+v, %v", observed, err)
		}
	}
}

type onceReadBackend struct {
	artifactmanifest.Backend
	reads          map[string]int
	newlyPublished string
}

func (b *onceReadBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	b.reads[key]++
	if b.reads[key] > 1 && key != b.newlyPublished {
		return nil, fmt.Errorf("%w: evidence consumer reopened an already captured source", artifactmanifest.ErrCorrupt)
	}
	return b.Backend.Get(ctx, key, limit)
}

func (b *onceReadBackend) Create(context.Context, string, []byte) (bool, error) {
	return false, errors.New("read admission must not write")
}

func TestReadUsageAndInspectUseOneCompletedSourcePass(t *testing.T) {
	backend, digest := usagePublication(t, wire(t, completeRecordedUsage()), Present, false)
	for _, usage := range []bool{false, true} {
		once := &onceReadBackend{Backend: backend, reads: make(map[string]int)}
		if usage {
			result, err := ReadUsage(context.Background(), once, digest)
			if err != nil || result.State != Present || result.Usage == nil {
				t.Fatalf("usage did not consume captured native bytes: %+v, %v", result, err)
			}
		} else {
			result, err := Inspect(context.Background(), once, digest)
			if err != nil || result.Integrity != "verified" || result.Document == nil {
				t.Fatalf("inspection reopened source objects: %+v, %v", result, err)
			}
		}
		if len(once.reads) < 5 {
			t.Fatal("fixture did not actually inspect sidecar, manifest, terminal, usage and final marker")
		}
		for _, reads := range once.reads {
			if reads != 1 {
				t.Fatal("a source object was not captured exactly once")
			}
		}
	}
}
