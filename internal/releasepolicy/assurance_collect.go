package releasepolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

type assuranceCollection struct {
	contract  *AssuranceContract
	data      []byte
	stream    *os.File
	rawStream *os.File
	rows      []AssuranceRow
	sync      func() error
	rawSync   func() error
}

// CollectAssured binds an explicitly selected independent profile before BEGIN.
// Source inspection and current review acceptance remain the producer's duty.
func CollectAssured(ctx context.Context, policyData, contractData []byte, directory string, collector Collector) error {
	p, err := DecodePolicy(policyData)
	if err != nil {
		return err
	}
	c, err := DecodeAssuranceContract(contractData, p)
	if err != nil {
		return err
	}
	if collector.BeforePublish == nil {
		return fmt.Errorf("assured collection requires a final current-source recheck")
	}
	bound := &assuranceCollection{contract: c, data: contractData}
	return collect(ctx, policyData, directory, collector, bound)
}

func (a *assuranceCollection) open(directory string) error {
	if err := writeExclusive(directory, "assurance-contract.json", a.data); err != nil {
		return err
	}
	stream, err := os.OpenFile(filepath.Join(directory, "assurance.ndjson"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	a.stream, a.sync = stream, stream.Sync
	rawStream, err := os.OpenFile(filepath.Join(directory, "assurance-rows.ndjson"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.Join(err, a.close())
	}
	a.rawStream, a.rawSync = rawStream, rawStream.Sync
	if err := a.rawSync(); err != nil {
		return errors.Join(err, a.close())
	}
	if err := a.sync(); err != nil {
		return errors.Join(err, a.close())
	}
	if err := syncDirectory(directory); err != nil {
		return errors.Join(err, a.close())
	}
	return nil
}

func (a *assuranceCollection) close() error {
	var err error
	if a.rawStream != nil {
		err = a.rawStream.Close()
		a.rawStream = nil
	}
	if a.stream != nil {
		err = errors.Join(err, a.stream.Close())
		a.stream = nil
	}
	return err
}

func (a *assuranceCollection) append(observation AttemptObservation) error {
	if observation.Output == nil {
		return fmt.Errorf("actual response output availability was not observed")
	}
	if err := validateAssuranceOutput(*observation.Output); err != nil {
		return err
	}
	if observation.Output.Availability == "available" &&
		observation.Result.Run.FinalOutput != *observation.Output.Value {
		return fmt.Errorf("observed response output differs from preserved run output")
	}
	digest, err := evidence.JSONDigest(observation.Result)
	if err != nil {
		return err
	}
	row := AssuranceRow{Kind: AssuranceRowKind, Version: AssuranceVersion, Sequence: len(a.rows) + 1,
		CollectionID: a.contract.Digest.SHA256, Key: observation.Summary.Key,
		Origin: observation.Result.Origin, RowDigest: *digest, Output: *observation.Output}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	actual, err := json.Marshal(observation.Result)
	if err != nil {
		return err
	}
	actual = append(actual, '\n')
	if _, err := a.rawStream.Write(actual); err != nil {
		return err
	}
	if err := a.rawSync(); err != nil {
		return err
	}
	if _, err := a.stream.Write(data); err != nil {
		return err
	}
	if err := a.sync(); err != nil {
		return err
	}
	a.rows = append(a.rows, row)
	return nil
}

func (a *assuranceCollection) finish(directory string, policy *Policy, journal *Journal) error {
	stream, err := readArtifact(directory, "assurance.ndjson")
	if err != nil {
		return err
	}
	ledger := AssuranceLedger{Kind: AssuranceLedgerKind, Version: AssuranceVersion,
		CollectionID: a.contract.Digest.SHA256, ContractDigest: a.contract.Digest,
		Rows: a.rows, CoreJournalDigest: journal.Digest, StreamDigest: SourceDigest(stream),
		RawResults: map[Arm]models.EvidenceDigest{}}
	for _, arm := range []Arm{Baseline, Candidate} {
		data, err := readArtifact(directory, string(arm)+".results.json")
		if err != nil {
			return err
		}
		ledger.RawResults[arm] = SourceDigest(data)
	}
	data, err := SealJSON(ledger)
	if err != nil {
		return err
	}
	if err := writeExclusive(directory, "assurance-ledger.json", data); err != nil {
		return err
	}
	_, _, err = ReadAssuranceLedger(directory, policy, a.contract)
	return err
}
