package assurance

import (
	"errors"
	"slices"
)

// Pinned b30f11 transition machine, deliberately independent of the extracted
// replay state. Shared strict event/ack parsers still admit the native tokens.
func qualificationOldDerivePrefix(manifest qualificationManifest, ack qualificationDocument, records []qualificationRecord) (qualificationPrefix, error) {
	if len(records) > qualificationArtifactLimit {
		return qualificationPrefix{}, errors.New("qualification: bounded prefix record count required")
	}
	prefix := qualificationPrefix{manifest: manifest, ack: ack, records: slices.Clone(records)}
	if err := qualificationMatchAck(manifest, manifest.document, 0, nil, nil, ack); err != nil {
		return prefix, err
	}
	previous := manifest.document.sha256()
	lastAck := ack.sha256()
	m, err := qualificationManifestValue(manifest)
	if err != nil {
		return prefix, err
	}
	stage, next := "run_admission", uint64(0)
	seenChallenges := map[string]bool{}
	cutoff := uint64(0)
	for i, record := range records {
		row, err := qualificationDecode[qualificationRecordWire](record.document.bytes(), qualificationDocumentLimit)
		if err != nil {
			return prefix, err
		}
		eventDoc, err := qualificationSeal(row.Event)
		if err != nil {
			return prefix, err
		}
		if _, err := qualificationParseEvent(eventDoc.bytes(), manifest); err != nil {
			return prefix, err
		}
		ackDoc, err := qualificationSeal(row.Acknowledgment)
		if err != nil {
			return prefix, err
		}
		event := row.Event
		if event.Sequence != uint64(i+1) || event.PreviousSHA256 != previous ||
			qualificationMatchAck(manifest, eventDoc, event.Sequence, new(previous), event.PayloadSHA256, ackDoc) != nil {
			return prefix, errors.New("qualification: exact contiguous prefix required")
		}
		if event.Type == "run_terminal" {
			if i != len(records)-1 || event.Completion.CompletedJobs != next ||
				(event.Completion.State == "passed" && stage != "complete") {
				return prefix, errors.New("qualification: invalid separate terminal suffix")
			}
			prefix.final = new(record)
			prefix.records = slices.Clone(records[:i])
			break
		}
		if stage == "stopped" || stage == "complete" {
			return prefix, errors.New("qualification: no records after stopped/complete cutoff")
		}
		switch event.Type {
		case "run_admission":
			if stage != "run_admission" {
				return prefix, errors.New("qualification: repeated admission")
			}
			stage = "before_job"
			if !event.Admission.Allowed {
				stage = "stopped"
			}
		case "currentness":
			req, receipt := event.CurrentnessRequest, event.CurrentnessAcknowledgment
			if seenChallenges[req.Challenge] || req.Stage != stage ||
				(req.Stage == "before_job" && (req.Ordinal == nil || *req.Ordinal != next)) {
				return prefix, errors.New("qualification: currentness stage/nonce/ordinal")
			}
			seenChallenges[req.Challenge] = true
			if !receipt.Current || !receipt.AssociationValid {
				stage = "stopped"
			} else {
				switch req.Stage {
				case "before_job":
					stage = "job_admission"
				case "after_cleanup":
					stage = "before_decision"
				case "before_decision":
					stage, cutoff = "complete", event.Sequence
				}
			}
		case "job_admission", "job_start", "job_terminal":
			if stage != event.Type || event.Ordinal == nil || *event.Ordinal != next {
				return prefix, errors.New("qualification: ordered job transitions")
			}
			switch event.Type {
			case "job_admission":
				stage = "job_start"
				if !event.Admission.Allowed {
					stage = "stopped"
				}
			case "job_start":
				stage = "job_terminal"
			case "job_terminal":
				next++
				stage = "before_job"
				if next == uint64(len(m.Jobs)) {
					stage = "after_cleanup"
				}
			}
		case "run_terminal":
			return prefix, errors.New("qualification: core cutoff excludes run_terminal and receipt")
		}
		previous, lastAck = eventDoc.sha256(), ackDoc.sha256()
	}
	value := qualificationPrefixWire{manifest.document.sha256(), uint64(len(prefix.records)), previous, lastAck}
	prefix.document, err = qualificationSeal(value)
	prefix.complete = cutoff > 0 && cutoff == uint64(len(prefix.records))
	prefix.nextStage, prefix.nextOrdinal = stage, next
	return prefix, err
}
