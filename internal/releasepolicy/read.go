package releasepolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

// ReadDecision admits independent sidecars and separately published results.
// Missing, partial, malformed or contradictory publications never strict-pass.
func ReadDecision(directory string) (Decision, error) {
	return readDecision(directory, nil)
}

// ReadSelectedDecision compares the selection with the same admitted policy
// used for assessment, rather than reopening the commitment independently.
func ReadSelectedDecision(directory string, selected models.EvidenceDigest) (Decision, error) {
	return readDecision(directory, &selected)
}

func readDecision(directory string, selected *models.EvidenceDigest) (Decision, error) {
	d := InitialDecision()
	data, err := readArtifact(directory, "policy.json")
	if err != nil {
		d.Compatibility.State = "invalid"
		return d, fmt.Errorf("reading precollection policy: %w", err)
	}
	p, err := DecodePolicy(data)
	if err != nil {
		d.Compatibility.State = "invalid"
		d.Compatibility.Reasons = append(d.Compatibility.Reasons, err.Error())
		return d, err
	}
	if selected != nil && p.Digest != *selected {
		d.Compatibility.State = "mismatched"
		return d, fmt.Errorf("selected policy identity differs from the precollection commitment")
	}
	d.PlannedClusters = len(p.Design.Clusters)
	if p.Requirements.Assurance {
		d.Assurance.Reasons = []string{"required independently attributable assurance verdict is missing"}
	} else {
		d.Assurance.State = "not_required"
	}
	d.Golden = assessGolden(p, map[Arm]Receipt{})
	d.Billing = assessBilling(p, map[Arm]Receipt{})
	data, err = readArtifact(directory, "journal.json")
	if err != nil {
		d.Completeness.State, d.Statistics.State, d.Operations.State = "missing", "inconclusive", "incomplete"
		if errors.Is(err, os.ErrNotExist) {
			if prefixErr := accountIncomplete(directory, p, &d); prefixErr != nil {
				d.Compatibility.State = "invalid"
				d.Compatibility.Reasons = append(d.Compatibility.Reasons, prefixErr.Error())
				return d, prefixErr
			}
		}
		return d, fmt.Errorf("reading completed shared journal (no history adoption/resume): %w", err)
	}
	j, err := DecodeJournal(data, p)
	if err != nil {
		d.Compatibility.State, d.Completeness.State, d.Statistics.State = "invalid", "partial", "inconclusive"
		d.Compatibility.Reasons = append(d.Compatibility.Reasons, err.Error())
		return d, err
	}
	expected, err := Replay(p, j)
	if err != nil {
		return d, err
	}
	stream, err := readArtifact(directory, "journal.ndjson")
	if err != nil {
		d.Completeness.State = "missing"
		return d, fmt.Errorf("reading durable event tape: %w", err)
	}
	digest := sha256.Sum256(stream)
	if j.StreamDigest.Encoding != "source-bytes" || j.StreamDigest.SHA256 != hex.EncodeToString(digest[:]) {
		d.Compatibility.State = "mismatched"
		return d, fmt.Errorf("durable event tape source-byte identity mismatch")
	}
	if len(stream) == 0 || stream[len(stream)-1] != '\n' {
		d.Completeness.State = "partial"
		return d, fmt.Errorf("durable event tape lacks its final line terminator")
	}
	lines := bytes.Split(stream[:len(stream)-1], []byte{'\n'})
	if len(lines) != len(j.Events) {
		d.Completeness.State = "partial"
		return d, fmt.Errorf("durable event tape count differs from sealed journal")
	}
	for i, line := range lines {
		if _, err := jsonutil.Parse(line); err != nil {
			d.Compatibility.State = "invalid"
			return d, err
		}
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&event); err != nil || !reflect.DeepEqual(event, j.Events[i]) {
			d.Compatibility.State = "mismatched"
			return d, fmt.Errorf("durable event tape event %d contradicts sealed journal", i+1)
		}
	}
	if err := accountPrefix(p, j, &d); err != nil {
		d.Compatibility.State = "invalid"
		return d, err
	}
	actual := map[Arm]Receipt{}
	for _, arm := range []Arm{Baseline, Candidate} {
		data, err := readArtifact(directory, string(arm)+".begin.json")
		if err != nil {
			d.Completeness.State = "missing"
			return d, fmt.Errorf("reading precollection %s BEGIN: %w", arm, err)
		}
		begin, err := DecodeReceipt(data)
		if err != nil {
			d.Compatibility.State = "invalid"
			return d, err
		}
		r := expected[arm]
		expectedBegin := Receipt{Kind: ReceiptKind, Version: Version, State: "begin", CollectionID: r.CollectionID,
			PolicyDigest: r.PolicyDigest, Arm: arm, EvalID: r.EvalID, PlanDigest: r.PlanDigest, Samples: r.Samples}
		expectedBegin.Digest = begin.Digest
		if !reflect.DeepEqual(*begin, expectedBegin) {
			d.Compatibility.State = "mismatched"
			return d, fmt.Errorf("%s BEGIN identity contradicts collection commitment", arm)
		}
		data, err = readArtifact(directory, string(arm)+".final.json")
		if err != nil {
			d.Completeness.State, d.Statistics.State = "missing", "inconclusive"
			return d, fmt.Errorf("reading final %s receipt: %w", arm, err)
		}
		receipt, err := DecodeReceipt(data)
		if err != nil {
			d.Compatibility.State = "invalid"
			return d, err
		}
		data, err = readArtifact(directory, string(arm)+".result-binding.json")
		if err != nil {
			d.Completeness.State = "missing"
			return d, fmt.Errorf("reading attributable %s results: %w", arm, err)
		}
		result, err := DecodeResult(data)
		if err != nil {
			d.Compatibility.State = "invalid"
			return d, err
		}
		if err := VerifyReceipt(*receipt, expected[arm], *result); err != nil {
			d.Compatibility.State, d.Statistics.State = "mismatched", "inconclusive"
			d.Compatibility.Reasons = append(d.Compatibility.Reasons, err.Error())
			return d, err
		}
		data, err = readArtifact(directory, string(arm)+".results.json")
		if err != nil {
			d.Completeness.State = "missing"
			return d, fmt.Errorf("reading actual %s result rows: %w", arm, err)
		}
		rows, err := DecodeResults(data)
		if err != nil {
			d.Compatibility.State = "invalid"
			return d, err
		}
		if err := VerifyResults(*receipt, *rows); err != nil {
			d.Compatibility.State = "mismatched"
			return d, err
		}
		actual[arm] = *receipt
	}
	return Assess(p, actual)
}

func DecodeResults(data []byte) (*Results, error) {
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, err
	}
	var result Results
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result.Kind != "waza.release-results" || result.Version != Version {
		return nil, fmt.Errorf("unsupported actual release results document")
	}
	return &result, nil
}

func DecodeResult(data []byte) (*ResultBinding, error) {
	if _, err := jsonutil.Parse(data); err != nil {
		return nil, err
	}
	var result ResultBinding
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if result.Kind != BindingKind || result.Version != Version {
		return nil, fmt.Errorf("unsupported release result projection")
	}
	return &result, nil
}
