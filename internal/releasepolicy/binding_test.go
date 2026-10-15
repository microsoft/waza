package releasepolicy

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestCanonicalCoefficientNormalization(t *testing.T) {
	for _, n := range []int{3, 12, 738} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			tokens := make([]json.Number, n)
			for i := range tokens {
				raw, err := json.Marshal(1 / float64(n))
				if err != nil {
					t.Fatal(err)
				}
				tokens[i] = json.Number(raw)
			}
			weights, err := canonicalWeights(tokens)
			if err != nil {
				t.Fatal(err)
			}
			want := new(big.Rat).SetFrac64(1, int64(n))
			total := new(big.Rat)
			for _, w := range weights {
				if w.Cmp(want) != 0 {
					t.Fatal("uniform plan must normalize to its exact declared equal allocation")
				}
				total.Add(total, w)
			}
			if total.Cmp(big.NewRat(1, 1)) != 0 {
				t.Fatal("canonical mass must exactly equal one")
			}
			if _, err := canonicalWeights(tokens[:n-1]); err == nil {
				t.Fatal("canonical normalization must not repair omitted evidence")
			}
		})
	}
	weights, err := canonicalWeights([]json.Number{"0.1", "0.2", "0.7"})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []*big.Rat{big.NewRat(1, 10), big.NewRat(1, 5), big.NewRat(7, 10)} {
		if weights[i].Cmp(want) != 0 {
			t.Fatal("ordinary declared relative allocation changed")
		}
	}
	if _, err := canonicalWeights([]json.Number{"0.5000000000000000000000000001", "0.5000000000000000000000000001"}); err == nil {
		t.Fatal("hidden coefficient precision must be rejected")
	}
}

func TestMissingDeclarationsCannotDefaultToOptOut(t *testing.T) {
	p := testPolicy(t, 8)
	object, err := objectJSON(testBytes(t, p))
	if err != nil {
		t.Fatal(err)
	}
	requirements, ok := object["requirements"].(map[string]any)
	if !ok {
		t.Fatal("test policy requirements are missing")
	}
	delete(requirements, "assurance")
	sealed, err := SealJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePolicy(sealed); err == nil {
		t.Fatal("absent assurance declaration became an implicit false/opt-out")
	}
	if err := requireFields(map[string]any{}, reflect.TypeFor[Settings](), "/settings"); err == nil {
		t.Fatal("missing effective per-task settings silently defaulted")
	}
	requirements["assurance"] = nil
	sealed, err = SealJSON(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePolicy(sealed); err == nil {
		t.Fatal("null assurance declaration became an implicit false/opt-out")
	}
}

func TestPublishedArtifactBinding(t *testing.T) {
	for _, name := range []string{"missing_stream", "truncated_stream", "missing_begin", "changed_begin", "changed_result", "changed_receipt",
		"missing_final", "missing_binding", "missing_rows"} {
		t.Run(name, func(t *testing.T) {
			p := testPolicy(t, 8)
			directory := collectTest(t, p, testCollector(p))
			switch name {
			case "missing_stream":
				if err := os.Remove(filepath.Join(directory, "journal.ndjson")); err != nil {
					t.Fatal(err)
				}

			case "truncated_stream":
				path := filepath.Join(directory, "journal.ndjson")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data[:len(data)-1], 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing_begin":
				if err := os.Remove(filepath.Join(directory, "baseline.begin.json")); err != nil {
					t.Fatal(err)
				}
			case "missing_final", "missing_binding", "missing_rows":
				file := map[string]string{
					"missing_final": "candidate.final.json", "missing_binding": "candidate.result-binding.json",
					"missing_rows": "candidate.results.json",
				}[name]
				if err := os.Remove(filepath.Join(directory, file)); err != nil {
					t.Fatal(err)
				}
			case "changed_begin", "changed_receipt":
				file := "baseline.begin.json"
				if name == "changed_receipt" {
					file = "baseline.final.json"
				}
				path := filepath.Join(directory, file)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var r Receipt
				if err := json.Unmarshal(data, &r); err != nil {
					t.Fatal(err)
				}
				r.EvalID = "different-collection-eval"
				data, err = SealJSON(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed_result":
				path := filepath.Join(directory, "baseline.results.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var r Results
				if err := json.Unmarshal(data, &r); err != nil {
					t.Fatal(err)
				}
				r.Rows[0].Run.Status = "failed"
				data, err = json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			d, err := ReadDecision(directory)
			if err == nil || d.Accepted {
				t.Fatalf("broken publication accepted: %+v err=%v", d, err)
			}
			if name == "missing_final" || name == "missing_binding" || name == "missing_rows" {
				for _, arm := range []Arm{Baseline, Candidate} {
					counts := d.Accounting[arm]
					if counts.StartedTrials != len(PlannedSamples(p)) || counts.CompleteAttempts != len(PlannedSamples(p)) ||
						counts.FirstRate != nil || counts.RetryRate != nil {
						t.Fatalf("verified tape counts lost or uncertified rates inferred: %+v", counts)
					}
				}
			}
		})
	}
}
