package assurance

import (
	"errors"
	"maps"
	"slices"
)

// Constructed after the operation's fresh manifest byte admission. Decoded
// native wire is not retained and this view never survives the operation.
type qualificationManifestView struct {
	manifest                                               qualificationManifest
	invocationID, inputsSHA256, contractSHA256, cid, armID string
	jobs                                                   []qualificationJobAdmission
	jobDocs                                                []qualificationDocument
	backend, currentness                                   qualificationDocument
}

func qualificationNewManifestView(manifest qualificationManifest) (*qualificationManifestView, error) {
	wire, err := qualificationManifestValue(manifest)
	if err != nil {
		return nil, err
	}
	return qualificationManifestViewFromDecoded(manifest, wire)
}

// wire is transferred from the private strict decoder, not caller metadata.
func qualificationManifestViewFromDecoded(manifest qualificationManifest, wire qualificationManifestWire) (*qualificationManifestView, error) {
	if manifest.document.canonical == "" {
		return nil, errors.New("qualification: bounded admitted manifest view required")
	}
	view := &qualificationManifestView{manifest: manifest, invocationID: wire.InvocationID,
		inputsSHA256: wire.InputsSHA256, contractSHA256: wire.Inputs.Association.ContractSHA256,
		cid: wire.Inputs.Association.CID, armID: wire.Inputs.Association.ArmID}
	var err error
	view.backend, err = qualificationSeal(wire.Inputs.BackendProfile)
	if err != nil {
		return nil, err
	}
	view.currentness, err = qualificationSeal(wire.Inputs.CurrentnessProfile)
	if err != nil {
		return nil, err
	}
	view.jobDocs = make([]qualificationDocument, len(wire.Jobs))
	view.jobs = make([]qualificationJobAdmission, len(wire.Jobs))
	for i, job := range wire.Jobs {
		view.jobs[i] = qualificationJobAdmission{job.Selector, job.ExpectedPassed, job.RequestedModel}
		view.jobDocs[i], err = qualificationSeal(job)
		if err != nil {
			return nil, err
		}
	}
	return view, nil
}

func qualificationParseManifestView(data []byte, admitted qualificationAdmittedInputContext) (qualificationManifest, *qualificationManifestView, error) {
	manifest, wire, err := qualificationParseManifestProjection(data, admitted, qualificationDocumentLimit, qualificationTotalLimit, nil, nil)
	if err != nil {
		return manifest, nil, err
	}
	view, err := qualificationManifestViewFromDecoded(manifest, wire)
	return manifest, view, err
}

type qualificationJobAdmission struct {
	Selector       qualificationSelector
	ExpectedPassed bool
	RequestedModel string
}

// Only value-only identity fields leave the view, never mutable native wire.
func (view *qualificationManifestView) job(ordinal uint64) (qualificationJobAdmission, qualificationDocument, error) {
	if ordinal >= uint64(len(view.jobs)) {
		return qualificationJobAdmission{}, qualificationDocument{}, errors.New("qualification: job ordinal absent")
	}
	return view.jobs[ordinal], view.jobDocs[ordinal], nil
}

type qualificationReplayState struct {
	view           *qualificationManifestView
	ack            qualificationDocument
	records        []qualificationRecord
	previous       string
	lastAck        string
	stage          string
	next           uint64
	seenChallenges map[string]bool
	cutoff         uint64
	final          *qualificationRecord
	advances       uint64
}

func qualificationBeginReplay(view *qualificationManifestView, ack qualificationDocument) (*qualificationReplayState, error) {
	if err := qualificationMatchAckView(view.manifest, view.manifest.document, 0, nil, nil, ack, view); err != nil {
		return nil, err
	}
	return &qualificationReplayState{view: view, ack: ack, records: []qualificationRecord{},
		previous: view.manifest.document.sha256(), lastAck: ack.sha256(),
		stage: "run_admission", seenChallenges: map[string]bool{}}, nil
}

func (state *qualificationReplayState) fork() *qualificationReplayState {
	copy := *state
	copy.records = slices.Clone(state.records)
	copy.seenChallenges = maps.Clone(state.seenChallenges)
	if state.final != nil {
		copy.final = new(*state.final)
	}
	return &copy
}

// The shared transition machine never seeds itself from a cached FS head.
func (state *qualificationReplayState) advance(record qualificationRecord) error {
	state.advances++
	if len(state.records) >= qualificationArtifactLimit || state.final != nil {
		return errors.New("qualification: invalid separate terminal suffix")
	}
	row, err := qualificationDecode[qualificationRecordWire](record.document.bytes(), qualificationDocumentLimit)
	if err != nil {
		return err
	}
	eventDoc, err := qualificationSeal(row.Event)
	if err != nil {
		return err
	}
	if _, err := qualificationParseEventBoundedView(eventDoc.bytes(), state.view.manifest,
		qualificationDocumentLimit, qualificationTotalLimit, nil, nil, state.view); err != nil {
		return err
	}
	ackDoc, err := qualificationSeal(row.Acknowledgment)
	if err != nil {
		return err
	}
	event := row.Event
	if event.Sequence != uint64(len(state.records)+1) || event.PreviousSHA256 != state.previous ||
		qualificationMatchAckView(state.view.manifest, eventDoc, event.Sequence, new(state.previous), event.PayloadSHA256, ackDoc, state.view) != nil {
		return errors.New("qualification: exact contiguous prefix required")
	}
	if event.Type == "run_terminal" {
		if event.Completion.CompletedJobs != state.next ||
			(event.Completion.State == "passed" && state.stage != "complete") {
			return errors.New("qualification: invalid separate terminal suffix")
		}
		state.final = new(record)
		return nil
	}
	if state.stage == "stopped" || state.stage == "complete" {
		return errors.New("qualification: no records after stopped/complete cutoff")
	}
	switch event.Type {
	case "run_admission":
		if state.stage != "run_admission" {
			return errors.New("qualification: repeated admission")
		}
		state.stage = "before_job"
		if !event.Admission.Allowed {
			state.stage = "stopped"
		}
	case "currentness":
		req, receipt := event.CurrentnessRequest, event.CurrentnessAcknowledgment
		if state.seenChallenges[req.Challenge] || req.Stage != state.stage ||
			(req.Stage == "before_job" && (req.Ordinal == nil || *req.Ordinal != state.next)) {
			return errors.New("qualification: currentness stage/nonce/ordinal")
		}
		state.seenChallenges[req.Challenge] = true
		if !receipt.Current || !receipt.AssociationValid {
			state.stage = "stopped"
		} else {
			switch req.Stage {
			case "before_job":
				state.stage = "job_admission"
			case "after_cleanup":
				state.stage = "before_decision"
			case "before_decision":
				state.stage, state.cutoff = "complete", event.Sequence
			}
		}
	case "job_admission", "job_start", "job_terminal":
		if state.stage != event.Type || event.Ordinal == nil || *event.Ordinal != state.next {
			return errors.New("qualification: ordered job transitions")
		}
		switch event.Type {
		case "job_admission":
			state.stage = "job_start"
			if !event.Admission.Allowed {
				state.stage = "stopped"
			}
		case "job_start":
			state.stage = "job_terminal"
		case "job_terminal":
			state.next++
			state.stage = "before_job"
			if state.next == uint64(len(state.view.jobs)) {
				state.stage = "after_cleanup"
			}
		}
	}
	state.previous, state.lastAck = eventDoc.sha256(), ackDoc.sha256()
	state.records = append(state.records, record)
	return nil
}

func (state *qualificationReplayState) prefix() (qualificationPrefix, error) {
	prefix := qualificationPrefix{manifest: state.view.manifest, ack: state.ack,
		records: slices.Clone(state.records), complete: state.cutoff > 0 && state.cutoff == uint64(len(state.records)),
		nextStage: state.stage, nextOrdinal: state.next}
	if state.final != nil {
		prefix.final = new(*state.final)
	}
	value := qualificationPrefixWire{state.view.manifest.document.sha256(),
		uint64(len(state.records)), state.previous, state.lastAck}
	document, err := qualificationSeal(value)
	prefix.document = document
	return prefix, err
}

func qualificationDerivePrefixView(manifest qualificationManifest, ack qualificationDocument, records []qualificationRecord, view *qualificationManifestView) (qualificationPrefix, error) {
	// Pure parsing retains supplied rows on error; recovery retains its last
	// verified partial prefix instead.
	rejected := qualificationPrefix{manifest: manifest, ack: ack, records: slices.Clone(records)}
	if len(records) > qualificationArtifactLimit {
		return qualificationPrefix{}, errors.New("qualification: bounded prefix record count required")
	}
	state, err := qualificationBeginReplay(view, ack)
	if err != nil {
		return rejected, err
	}
	for i, record := range records {
		if err := state.advance(record); err != nil {
			return rejected, err
		}
		if state.final != nil && i != len(records)-1 {
			return rejected, errors.New("qualification: invalid separate terminal suffix")
		}
	}
	return state.prefix()
}
