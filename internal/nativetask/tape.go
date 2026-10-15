package nativetask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/releasepolicy"
)

const (
	payloadFile = "native-task-rows.ndjson"
	eventFile   = "native-task-events.ndjson"
)

// Tape only acknowledges local payload/event persistence. It does not append a
// core terminal, publish a final ledger, or prove a producer executed anything.
type Tape struct {
	root                    *os.Root
	payload, events         *os.File
	syncPayload, syncEvents func() error
	active                  *Admitted
	sequence                int
	poisoned                bool
	started                 map[releasepolicy.AttemptKey]bool
	collection              string
}

func syncRoot(root *os.Root) (err error) {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	return directory.Sync()
}

func CreateTape(ctx context.Context, parent *os.Root, name string) (_ *Tape, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("private durable native tape creation is unsupported on %s; use Linux or macOS for creation; reading supplied tapes remains available", runtime.GOOS)
	}
	if parent == nil || name == "" || name == "." || name == ".." || bytes.ContainsAny([]byte(name), "/\\") {
		return nil, fmt.Errorf("native tape needs a new direct evaluator-owned directory")
	}
	if err := parent.Mkdir(name, 0o700); err != nil {
		return nil, fmt.Errorf("reserving native tape: %w", err)
	}
	if err := syncRoot(parent); err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	tape := &Tape{root: root, started: map[releasepolicy.AttemptKey]bool{}}
	defer func() {
		if err != nil {
			err = errors.Join(err, tape.Close())
		}
	}()
	tape.payload, err = root.OpenFile(payloadFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	tape.events, err = root.OpenFile(eventFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	tape.syncPayload, tape.syncEvents = tape.payload.Sync, tape.events.Sync
	if err := tape.syncPayload(); err != nil {
		return nil, err
	}
	if err := tape.syncEvents(); err != nil {
		return nil, err
	}
	if err := syncRoot(root); err != nil {
		return nil, err
	}
	return tape, nil
}

func writeSynced(file *os.File, data []byte, sync func() error) error {
	n, err := file.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return sync()
}

func (t *Tape) appendEvent(event Event) error {
	event.Kind, event.Version, event.Sequence = eventKind, version, t.sequence+1
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := writeSynced(t.events, append(data, '\n'), t.syncEvents); err != nil {
		t.poisoned = true
		return err
	}
	t.sequence++
	return nil
}

func (t *Tape) Start(ctx context.Context, admitted *Admitted) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || t.root == nil || t.poisoned || t.active != nil {
		return fmt.Errorf("native tape cannot admit another start")
	}
	binding, err := admitted.Binding()
	if err != nil {
		return err
	}
	if t.started[binding.Key] || (t.collection != "" && t.collection != binding.CollectionID) {
		return fmt.Errorf("native start is repeated or belongs to another collection")
	}
	if err := t.appendEvent(Event{Type: "start", Admission: binding}); err != nil {
		return err
	}
	t.active = admitted
	t.started[binding.Key], t.collection = true, binding.CollectionID
	return nil
}

// Complete requires full raw admission BEFORE any write. A failure poisons the
// tape: no truncation, repair, next attempt or successful terminal is permitted.
func (t *Tape) Complete(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || t.root == nil || t.poisoned || t.active == nil {
		return fmt.Errorf("native tape has no writable active start")
	}
	record, err := DecodeRecord(raw, t.active)
	if err != nil {
		return err
	}
	if err := writeSynced(t.payload, append(bytes.Clone(raw), '\n'), t.syncPayload); err != nil {
		t.poisoned = true
		return err
	}
	binding, err := t.active.Binding()
	if err != nil {
		t.poisoned = true
		return err
	}
	digest, row, err := recordDigests(raw)
	if err != nil {
		t.poisoned = true
		return err
	}
	if err := t.appendEvent(Event{Type: "terminal", Admission: binding, State: record.Summary.Category,
		RecordDigest: digest, RowDigest: row}); err != nil {
		return err
	}
	t.active = nil
	return nil
}

func recordDigests(raw []byte) (*models.EvidenceDigest, *models.EvidenceDigest, error) {
	value, err := jsonutil.Parse(raw)
	if err != nil {
		return nil, nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("native payload must be an object")
	}
	digest, err := evidence.JSONDigest(value)
	if err != nil {
		return nil, nil, err
	}
	row, err := evidence.JSONDigest(object["actual_row"])
	return digest, row, err
}

func (t *Tape) Close() error {
	if t == nil {
		return nil
	}
	var err error
	if t.payload != nil {
		err = t.payload.Close()
		t.payload = nil
	}
	if t.events != nil {
		err = errors.Join(err, t.events.Close())
		t.events = nil
	}
	if t.root != nil {
		err = errors.Join(err, t.root.Close())
		t.root = nil
	}
	return err
}

type Prefix struct {
	Records []Record
	Pending *Admission
}

// ReadPrefix checks local structure only. Pending/crashed collections are not
// complete evidence; there is no final ledger or pass field in this primitive.
func ReadPrefix(ctx context.Context, root *os.Root, admissions []*Admitted) (*Prefix, error) {
	if root == nil {
		return nil, fmt.Errorf("native prefix requires an evaluator root")
	}
	events, err := readSource(ctx, Source{root, eventFile})
	if err != nil {
		return nil, err
	}
	payload, err := readSource(ctx, Source{root, payloadFile})
	if err != nil {
		return nil, err
	}
	return verifyPrefix(ctx, events, payload, admissions)
}

func lines(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, fmt.Errorf("native tape is torn or missing final newline")
	}
	parts := bytes.Split(data[:len(data)-1], []byte{'\n'})
	for _, part := range parts {
		if len(part) == 0 {
			return nil, fmt.Errorf("native tape has empty trailing or internal payload")
		}
	}
	return parts, nil
}

func verifyPrefix(ctx context.Context, events, payload []byte, admissions []*Admitted) (*Prefix, error) {
	eventLines, err := lines(events)
	if err != nil {
		return nil, err
	}
	payloadLines, err := lines(payload)
	if err != nil {
		return nil, err
	}
	result := &Prefix{Records: []Record{}}
	seen := map[releasepolicy.AttemptKey]bool{}
	collection := ""
	for _, admission := range admissions {
		binding, err := admission.Binding()
		if err != nil {
			return nil, err
		}
		if seen[binding.Key] || (collection != "" && collection != binding.CollectionID) {
			return nil, fmt.Errorf("native expected allocation is repeated or mixed-collection")
		}
		seen[binding.Key], collection = true, binding.CollectionID
	}
	var active *Admitted
	next := 0
	for i, line := range eventLines {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		var event Event
		if err := decodeExact(line, &event); err != nil {
			return result, err
		}
		value, err := jsonutil.Parse(line)
		if err != nil {
			return result, err
		}
		object, ok := value.(map[string]any)
		if !ok {
			return result, fmt.Errorf("native event must be an object")
		}
		_, state := object["state"]
		_, recordDigest := object["record_digest"]
		_, rowDigest := object["row_digest"]
		if event.Kind != eventKind || event.Version != version || event.Sequence != i+1 {
			return result, fmt.Errorf("native event sequence/kind/version mismatch")
		}
		switch event.Type {
		case "start":
			if active != nil || state || recordDigest || rowDigest || next >= len(admissions) {
				return result, fmt.Errorf("native start is extra, overlapping or has terminal fields")
			}
			binding, err := admissions[next].Binding()
			if err != nil {
				return result, err
			}
			if event.Admission != binding {
				return result, fmt.Errorf("native start allocation was substituted")
			}
			active = admissions[next]
			result.Pending = &binding
		case "terminal":
			if active == nil || !state || !recordDigest || !rowDigest || event.RecordDigest == nil || event.RowDigest == nil ||
				!validDigest(*event.RecordDigest, "json-v1") || !validDigest(*event.RowDigest, "json-v1") ||
				next >= len(payloadLines) {
				return result, fmt.Errorf("native terminal lacks complete active payload")
			}
			binding, err := active.Binding()
			if err != nil {
				return result, err
			}
			record, err := DecodeRecord(payloadLines[next], active)
			if err != nil {
				return result, err
			}
			digest, row, err := recordDigests(payloadLines[next])
			if err != nil {
				return result, err
			}
			if event.Admission != binding || event.State != record.Summary.Category || *event.RecordDigest != *digest || *event.RowDigest != *row {
				return result, fmt.Errorf("native terminal/payload binding mismatch")
			}
			result.Records = append(result.Records, *record)
			result.Pending = nil
			active = nil
			next++
		default:
			return result, fmt.Errorf("unknown native event")
		}
	}
	if next != len(payloadLines) {
		return result, fmt.Errorf("native payload has an orphan or extra record")
	}
	return result, nil
}
