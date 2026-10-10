package assurance

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/jsonutil"
)

type qualificationArtifactRead struct {
	Role    string
	Ordinal *uint64
	Root    *os.Root
}
type qualificationArtifactAcquisition struct {
	artifacts []qualificationArtifact
	originals map[string]string
}
type qualificationPrefixWire struct {
	ManifestSHA256           string `json:"manifest_sha256"`
	LastSequence             uint64 `json:"last_sequence"`
	LastEventSHA256          string `json:"last_event_sha256"`
	LastAcknowledgmentSHA256 string `json:"last_acknowledgment_sha256"`
}
type qualificationRecordWire struct {
	Event          qualificationEventWire      `json:"event"`
	Acknowledgment qualificationAcknowledgment `json:"acknowledgment"`
}
type qualificationCoreWire struct {
	Kind                   string                      `json:"kind"`
	Version                string                      `json:"version"`
	Prefix                 qualificationPrefixWire     `json:"prefix"`
	Manifest               qualificationManifestWire   `json:"manifest"`
	ManifestAcknowledgment qualificationAcknowledgment `json:"manifest_acknowledgment"`
	Records                []qualificationRecordWire   `json:"records"`
}
type qualificationTapeRow struct {
	Ordinal               uint64                  `json:"ordinal"`
	JobSHA256             string                  `json:"job_sha256"`
	Selector              qualificationSelector   `json:"selector"`
	Currentness           qualificationRecordWire `json:"currentness"`
	Admission             qualificationRecordWire `json:"admission"`
	Start                 qualificationRecordWire `json:"start"`
	Terminal              qualificationRecordWire `json:"terminal"`
	TerminalPayloadSHA256 string                  `json:"terminal_payload_sha256"`
}
type qualificationTapeWire struct {
	Kind    string                  `json:"kind"`
	Version string                  `json:"version"`
	Prefix  qualificationPrefixWire `json:"prefix"`
	Rows    []qualificationTapeRow  `json:"rows"`
}
type qualificationArtifactIdentity struct {
	Role       string  `json:"role"`
	Ordinal    *uint64 `json:"ordinal"`
	Encoding   string  `json:"encoding"`
	ByteLength uint64  `json:"byte_length"`
	SHA256     string  `json:"sha256"`
}
type qualificationInventoryWire struct {
	Kind           string                          `json:"kind"`
	Version        string                          `json:"version"`
	InvocationID   string                          `json:"invocation_id"`
	ContractSHA256 string                          `json:"contract_sha256"`
	CID            string                          `json:"cid"`
	ArmID          string                          `json:"arm_id"`
	Prefix         qualificationPrefixWire         `json:"prefix"`
	Artifacts      []qualificationArtifactIdentity `json:"artifacts"`
}
type qualificationPrefix struct {
	document    qualificationDocument
	manifest    qualificationManifest
	ack         qualificationDocument
	records     []qualificationRecord
	complete    bool
	pending     []qualificationDocument
	final       *qualificationRecord
	nextStage   string
	nextOrdinal uint64
}
type qualificationInventory struct{ document qualificationDocument }

func qualificationArtifactFilename(role string, ordinal *uint64) (string, error) {
	switch role {
	case "actual_calibration_report", "core_journal", "ordered_job_tape":
		if ordinal == nil {
			return role + ".json", nil
		}
	case "job_terminal_payload":
		if ordinal != nil && *ordinal < 4096 {
			return fmt.Sprintf("job_terminal_payload-%04d.json", *ordinal), nil
		}
	}
	return "", errors.New("qualification: fixed artifact role/ordinal required")
}
func qualificationArtifactKey(artifact qualificationArtifact) string {
	name, _ := qualificationArtifactFilename(artifact.Role, artifact.Ordinal)
	return name
}
func qualificationMakeArtifact(role string, ordinal *uint64, data []byte) qualificationArtifact {
	encoding := "json-v1"
	if role == "actual_calibration_report" {
		encoding = "source-bytes-sha256"
	}
	var detached *uint64
	if ordinal != nil {
		detached = new(*ordinal)
	}
	return qualificationArtifact{Role: role, Ordinal: detached, Encoding: encoding,
		ByteLength: uint64(len(data)), SHA256: byteSHA256(data), BytesBase64: base64.StdEncoding.EncodeToString(data)}
}
func qualificationAcquireArtifacts(ctx context.Context, reads []qualificationArtifactRead) (qualificationArtifactAcquisition, error) {
	return qualificationAcquireArtifactsBounded(ctx, reads, qualificationDocumentLimit, qualificationTotalLimit, nil)
}
func qualificationAcquireArtifactsBounded(ctx context.Context, reads []qualificationArtifactRead, documentLimit int, totalLimit uint64, materializing func()) (qualificationArtifactAcquisition, error) {
	if documentLimit < 1 || documentLimit > qualificationDocumentLimit || totalLimit < 1 || totalLimit > qualificationTotalLimit {
		return qualificationArtifactAcquisition{}, errors.New("qualification: fixed acquisition ceiling required")
	}
	if len(reads) == 0 || len(reads) > qualificationArtifactLimit {
		return qualificationArtifactAcquisition{}, errors.New("qualification: artifact acquisition count")
	}
	acquired := qualificationArtifactAcquisition{artifacts: []qualificationArtifact{}, originals: map[string]string{}}
	total := uint64(0)
	for _, read := range reads {
		name, err := qualificationArtifactFilename(read.Role, read.Ordinal)
		if err != nil || read.Root == nil {
			return acquired, errors.New("qualification: rooted fixed artifact required")
		}
		if _, exists := acquired.originals[name]; exists {
			return acquired, errors.New("qualification: duplicate artifact acquisition")
		}
		data, err := qualificationSecureRead(ctx, read.Root, name, min(uint64(documentLimit), totalLimit-total))
		if err != nil {
			return acquired, err
		}
		total += uint64(len(data))
		if read.Role != "actual_calibration_report" {
			if err := qualificationProtocolBlobPreflight(data, documentLimit, totalLimit, nil); err != nil {
				return acquired, err
			}
		}
		if materializing != nil {
			materializing()
		}
		artifact := qualificationMakeArtifact(read.Role, read.Ordinal, data)
		if _, err := qualificationValidateArtifact(artifact, qualificationDocumentLimit, nil); err != nil {
			return acquired, err
		}
		if read.Role == "actual_calibration_report" {
			if _, err := qualificationOriginalReport(artifact); err != nil {
				return acquired, err
			}
		}
		acquired.originals[name] = string(data)
		acquired.artifacts = append(acquired.artifacts, artifact)
	}
	slices.SortFunc(acquired.artifacts, func(a, b qualificationArtifact) int {
		return strings.Compare(qualificationArtifactKey(a), qualificationArtifactKey(b))
	})
	return acquired, nil
}
func (acquired qualificationArtifactAcquisition) original(role string, ordinal *uint64) ([]byte, error) {
	name, err := qualificationArtifactFilename(role, ordinal)
	if err != nil {
		return nil, err
	}
	data, ok := acquired.originals[name]
	if !ok {
		return nil, errors.New("qualification: original artifact absent")
	}
	return []byte(data), nil
}

func qualificationMatchAck(manifest qualificationManifest, event qualificationDocument, sequence uint64, previous, payload *string, ack qualificationDocument) error {
	view, err := qualificationNewManifestView(manifest)
	if err != nil {
		return err
	}
	return qualificationMatchAckView(manifest, event, sequence, previous, payload, ack, view)
}
func qualificationMatchAckView(manifest qualificationManifest, event qualificationDocument, sequence uint64, previous, payload *string, ack qualificationDocument, view *qualificationManifestView) error {
	if view.manifest.document.canonical != manifest.document.canonical {
		return errors.New("qualification: operation view exact manifest mismatch")
	}
	value, ackErr := qualificationDecode[qualificationAcknowledgment](ack.bytes(), qualificationDocumentLimit)
	profile := view.backend
	if ackErr != nil {
		return errors.New("qualification: acknowledgment correspondence unavailable")
	}
	if _, err := qualificationParseAcknowledgment(ack.bytes()); err != nil {
		return err
	}
	equals := func(a, b *string) bool { return a == nil && b == nil || equalPointer(a, b) }
	if value.InvocationID != view.invocationID || value.ManifestSHA256 != manifest.document.sha256() ||
		value.BackendProfileSHA256 != profile.sha256() || value.Sequence != sequence || value.EventSHA256 != event.sha256() ||
		!equals(value.PreviousSHA256, previous) || !equals(value.PayloadSHA256, payload) {
		return errors.New("qualification: exact acknowledgment mismatch")
	}
	return nil
}

func qualificationDerivePrefix(manifest qualificationManifest, ack qualificationDocument, records []qualificationRecord) (qualificationPrefix, error) {
	if len(records) > qualificationArtifactLimit {
		return qualificationPrefix{}, errors.New("qualification: bounded prefix record count required")
	}
	rejected := qualificationPrefix{manifest: manifest, ack: ack, records: slices.Clone(records)}
	view, err := qualificationNewManifestView(manifest)
	if err != nil {
		return rejected, err
	}
	return qualificationDerivePrefixView(manifest, ack, records, view)
}

func qualificationDeriveCoreAndTape(prefix qualificationPrefix, terminals []qualificationTerminal) ([]qualificationArtifact, error) {
	if !prefix.complete || len(prefix.pending) != 0 {
		return nil, errors.New("qualification: incomplete prefix has no complete artifacts")
	}
	m, err := qualificationManifestValue(prefix.manifest)
	if err != nil || len(terminals) != len(m.Jobs) {
		return nil, errors.New("qualification: terminal set must be bijective")
	}
	p, err := qualificationDecode[qualificationPrefixWire](prefix.document.bytes(), qualificationDocumentLimit)
	if err != nil {
		return nil, err
	}
	ack, err := qualificationDecode[qualificationAcknowledgment](prefix.ack.bytes(), qualificationDocumentLimit)
	if err != nil {
		return nil, err
	}
	core := qualificationCoreWire{"waza.qualification-core-journal-prefix", qualificationVersion, p, m, ack, []qualificationRecordWire{}}
	tape := qualificationTapeWire{"waza.qualification-ordered-job-tape-prefix", qualificationVersion, p, []qualificationTapeRow{}}
	rows := make([]qualificationTapeRow, len(m.Jobs))
	for _, record := range prefix.records {
		row, err := qualificationDecode[qualificationRecordWire](record.document.bytes(), qualificationDocumentLimit)
		if err != nil {
			return nil, err
		}
		core.Records = append(core.Records, row)
		if row.Event.Ordinal == nil {
			continue
		}
		index := *row.Event.Ordinal
		job, digest, err := qualificationJobAt(prefix.manifest, index)
		if err != nil {
			return nil, err
		}
		rows[index].Ordinal, rows[index].JobSHA256, rows[index].Selector = index, digest.sha256(), job.Selector
		switch row.Event.Type {
		case "currentness":
			rows[index].Currentness = row
		case "job_admission":
			rows[index].Admission = row
		case "job_start":
			rows[index].Start = row
		case "job_terminal":
			rows[index].Terminal, rows[index].TerminalPayloadSHA256 = row, *row.Event.PayloadSHA256
		}
	}
	artifacts := []qualificationArtifact{}
	seen := map[uint64]bool{}
	for _, terminal := range terminals {
		parsed, err := qualificationParseTerminal(terminal.document.bytes(), prefix.manifest)
		if err != nil {
			return nil, err
		}
		value, err := qualificationDecode[qualificationTerminalWire](parsed.document.bytes(), qualificationDocumentLimit)
		if err != nil || seen[value.Ordinal] || rows[value.Ordinal].TerminalPayloadSHA256 != parsed.document.sha256() {
			return nil, errors.New("qualification: terminal payload correspondence")
		}
		if value.ObservationState != "observed" {
			return nil, errors.New("qualification: operational payload cannot complete an observation inventory")
		}
		seen[value.Ordinal] = true
		artifacts = append(artifacts, qualificationMakeArtifact("job_terminal_payload", new(value.Ordinal), parsed.document.bytes()))
	}
	tape.Rows = rows
	for _, item := range []struct {
		role string
		wire any
	}{{"core_journal", core}, {"ordered_job_tape", tape}} {
		document, err := qualificationSeal(item.wire)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, qualificationMakeArtifact(item.role, nil, document.bytes()))
	}
	return artifacts, nil
}

func qualificationDeriveInventory(prefix qualificationPrefix, acquired qualificationArtifactAcquisition) (qualificationInventory, error) {
	if !prefix.complete || len(prefix.pending) != 0 {
		return qualificationInventory{}, errors.New("qualification: incomplete prefix has no inventory")
	}
	m, err := qualificationManifestValue(prefix.manifest)
	if err != nil || len(acquired.artifacts) != len(m.Jobs)+3 {
		return qualificationInventory{}, errors.New("qualification: complete artifact set required")
	}
	terminals := []qualificationTerminal{}
	reportFound := false
	var actualReport *Report
	var reportBytes []byte
	seen := map[string]bool{}
	for _, artifact := range acquired.artifacts {
		key := qualificationArtifactKey(artifact)
		if seen[key] {
			return qualificationInventory{}, errors.New("qualification: duplicate inventory role")
		}
		seen[key] = true
		data, err := acquired.original(artifact.Role, artifact.Ordinal)
		if err != nil || byteSHA256(data) != artifact.SHA256 || uint64(len(data)) != artifact.ByteLength {
			return qualificationInventory{}, errors.New("qualification: acquired original byte mismatch")
		}
		if artifact.Role == "job_terminal_payload" {
			terminal, err := qualificationParseTerminal(data, prefix.manifest)
			if err != nil {
				return qualificationInventory{}, err
			}
			terminals = append(terminals, terminal)
		}
		if artifact.Role == "actual_calibration_report" {
			actualReport, err = qualificationOriginalReport(artifact)
			if err != nil {
				return qualificationInventory{}, err
			}
			reportBytes = data
			reportFound = true
		}
	}
	if !reportFound {
		return qualificationInventory{}, errors.New("qualification: original report missing")
	}
	labelsSHA := byteSHA256(mustSource(prefix.manifest.context.sources, "references"))
	var reviewed SuppliedReview
	if err := qualificationDecodeNative(m.Inputs.SuppliedReview, &reviewed); err != nil || reviewed.Decision == nil ||
		actualReport.Review.SourceID != reviewed.SourceID || !actualReport.Review.CurrentSourceAccepted ||
		!actualReport.Review.Eligible || actualReport.Review.DeclaredState != reviewed.Decision.State {
		return qualificationInventory{}, errors.New("qualification: original report differs from admitted review custody")
	}
	if actualReport.LabelsSHA256 != labelsSHA || actualReport.Calibration.Protocol != m.Plan.Protocol ||
		actualReport.Calibration.Model != m.Plan.Model || actualReport.Calibration.MaxJudgeExecutions != m.Plan.MaxJudgeExecutions ||
		actualReport.Calibration.ExecutionLedger == nil || len(*actualReport.Calibration.ExecutionLedger) != len(m.Jobs) {
		return qualificationInventory{}, errors.New("qualification: original actual report not bound to this source/job inventory")
	}
	expectedBindings := map[string]string{
		"eval_source_bytes":            m.Inputs.ArmInput.SourceInventory.EvalSourceSHA256,
		"eval_resolved_config_json_v1": m.Inputs.ArmInput.SourceInventory.ResolvedSpecSHA256,
	}
	for _, source := range m.Inputs.Sources {
		if source.Role == "implementation_executable" {
			expectedBindings["implementation_executable_bytes"] = source.SHA256
		}
	}
	for domain, expected := range expectedBindings {
		count := 0
		for _, binding := range actualReport.Bindings {
			if binding.Domain == domain {
				count++
				if !binding.Applicable || binding.Expected == nil || binding.Actual == nil ||
					*binding.Expected != expected || *binding.Actual != expected || binding.State != "verified" {
					return qualificationInventory{}, errors.New("qualification: actual report native source binding differs")
				}
			}
		}
		if count != 1 {
			return qualificationInventory{}, errors.New("qualification: exact report source binding required")
		}
	}
	rawReport, err := jsonutil.Parse(reportBytes)
	if err != nil {
		return qualificationInventory{}, err
	}
	reportObject, ok := rawReport.(map[string]any)
	if !ok {
		return qualificationInventory{}, errors.New("qualification: actual report object required")
	}
	calibrationObject, ok := reportObject["calibration"].(map[string]any)
	if !ok {
		return qualificationInventory{}, errors.New("qualification: actual calibration object required")
	}
	ledger, ok := calibrationObject["execution_ledger"].([]any)
	if !ok || len(ledger) != len(m.Jobs) {
		return qualificationInventory{}, errors.New("qualification: exact original report ledger required")
	}
	for _, terminal := range terminals {
		value, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
		if err != nil {
			return qualificationInventory{}, err
		}
		rawExecution, err := qualificationSeal(ledger[value.Ordinal])
		expected, expectedErr := qualificationCanonical(value.Execution)
		if err != nil || expectedErr != nil || !bytes.Equal(rawExecution.bytes(), expected) {
			return qualificationInventory{}, errors.New("qualification: actual report ledger differs from terminal evidence")
		}
	}
	derived, err := qualificationDeriveCoreAndTape(prefix, terminals)
	if err != nil {
		return qualificationInventory{}, err
	}
	for _, artifact := range derived {
		data, err := acquired.original(artifact.Role, artifact.Ordinal)
		expected, decodeErr := base64.StdEncoding.Strict().DecodeString(artifact.BytesBase64)
		if err != nil || decodeErr != nil || !bytes.Equal(data, expected) {
			return qualificationInventory{}, errors.New("qualification: canonical actual role projection differs")
		}
	}
	p, err := qualificationDecode[qualificationPrefixWire](prefix.document.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationInventory{}, err
	}
	inventory := qualificationInventoryWire{Kind: "waza.qualification-artifact-inventory", Version: qualificationVersion,
		InvocationID: m.InvocationID, ContractSHA256: m.Inputs.Association.ContractSHA256, CID: m.Inputs.Association.CID,
		ArmID: m.Inputs.Association.ArmID, Prefix: p, Artifacts: []qualificationArtifactIdentity{}}
	for _, artifact := range acquired.artifacts {
		var ordinal *uint64
		if artifact.Ordinal != nil {
			ordinal = new(*artifact.Ordinal)
		}
		inventory.Artifacts = append(inventory.Artifacts, qualificationArtifactIdentity{artifact.Role, ordinal, artifact.Encoding, artifact.ByteLength, artifact.SHA256})
	}
	slices.SortFunc(inventory.Artifacts, func(a, b qualificationArtifactIdentity) int {
		if result := strings.Compare(a.Role, b.Role); result != 0 {
			return result
		}
		if a.Ordinal == nil || b.Ordinal == nil {
			return 0
		}
		return int(*a.Ordinal) - int(*b.Ordinal)
	})
	document, err := qualificationSeal(inventory)
	if err == nil && prefix.final != nil {
		final, decodeErr := qualificationDecode[qualificationRecordWire](prefix.final.document.bytes(), qualificationDocumentLimit)
		if decodeErr != nil {
			return qualificationInventory{}, decodeErr
		}
		completion := final.Event.Completion
		if completion.ActualReportSHA256 != nil && *completion.ActualReportSHA256 != byteSHA256(reportBytes) ||
			completion.ArtifactInventorySHA256 != nil && *completion.ArtifactInventorySHA256 != document.sha256() ||
			completion.State == "passed" && actualReport.State != AssessmentPassed {
			return qualificationInventory{}, errors.New("qualification: separate terminal differs from original report/inventory evidence")
		}
	}
	return qualificationInventory{document}, err
}
