package faultfixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/faultsequence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
)

var ErrUnmatched = errors.New("configured fault has no matching response; arguments omitted")

// Fragment retains source presence and scalar types across private subprocesses.
type Fragment struct {
	Format Format `json:"format"`
	Data   []byte `json:"data"`
}

// Envelope is private adapter configuration, not a public eval or evidence schema.
// Eval is used only for canonical scenario eligibility, not whole-eval validation.
type Envelope struct {
	Eval      []byte            `json:"eval"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Tool      string            `json:"tool,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Responses []Fragment        `json:"responses"`
	Fixtures  map[string][]byte `json:"fixtures,omitempty"`
}

type Transition string

const (
	Reserved  Transition = "reserved"
	Delivered Transition = "delivered"
	Canceled  Transition = "canceled"
	Failed    Transition = "failed"
)

// Observer closures bind only identities actually available at the native call
// site. The adapter never invents identities or associates SDK and MCP IDs.
// A step of -1 means no finite allocation; callbacks receive no argument payloads.
// Callbacks are synchronous; the native caller owns cancellation of callback I/O.
type Observer func(matcher, step int, transition Transition) error

func DecodeEnvelope(data []byte) (Envelope, error) {
	if _, err := jsonutil.Parse(data); err != nil {
		return Envelope{}, fmt.Errorf("decoding private fault configuration: %w", err)
	}
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decoding private fault configuration: %w", err)
	}
	if _, err := validateEnvelope(envelope); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func snapshotEnvelope(envelope Envelope) (Envelope, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return Envelope{}, fmt.Errorf("snapshotting private fault configuration: %w", err)
	}
	return DecodeEnvelope(data)
}

func validateEnvelope(envelope Envelope) (string, error) {
	version, scenario, err := models.ClassifyEvalSpec(envelope.Eval, "private fault configuration")
	if err != nil {
		return "", err
	}
	if version != models.ScenarioSchemaVersion || scenario == "" {
		return "", fmt.Errorf("private fault adapters require an explicit scenario schemaVersion %s", models.ScenarioSchemaVersion)
	}
	if strings.TrimSpace(envelope.Name) == "" || len(envelope.Responses) == 0 {
		return "", fmt.Errorf("private fault configuration requires a name and responses")
	}
	var validate func([]byte, Format, string) error
	switch envelope.Kind {
	case "command":
		if envelope.Tool != "" {
			return "", fmt.Errorf("command fault configuration must not define a tool")
		}
		validate = ValidateCommandResponse
	case "mcp":
		if strings.TrimSpace(envelope.Tool) == "" || envelope.Workspace != "" {
			return "", fmt.Errorf("MCP fault configuration requires a tool and no CLI workspace")
		}
		validate = ValidateMCPResponse
	default:
		return "", fmt.Errorf("unsupported private fault kind %q", envelope.Kind)
	}
	for index, fragment := range envelope.Responses {
		if err := validate(fragment.Data, fragment.Format, version); err != nil {
			return "", fmt.Errorf("fault response[%d]: %w", index, err)
		}
	}
	return version, nil
}

// bindConfiguration publishes a complete immutable configuration atomically.
// Changed configurations sharing an owner fail instead of silently resetting.
func bindConfiguration(root string, envelope Envelope) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("fault adapter state must be an absolute private directory")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("checking fault adapter state: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fault adapter state must be a private directory")
	}
	owner, err := json.Marshal([]string{envelope.Kind, envelope.Name, envelope.Tool})
	if err != nil {
		return "", fmt.Errorf("encoding fault owner: %w", err)
	}
	namespace := filepath.Join(root, fmt.Sprintf("%x", sha256.Sum256(owner)))
	if err := os.Mkdir(namespace, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("creating fault owner state: %w", err)
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encoding private fault configuration: %w", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	file, err := os.CreateTemp(namespace, ".binding-*")
	if err != nil {
		return "", fmt.Errorf("creating fault configuration binding: %w", err)
	}
	temporary := file.Name()
	if _, err := file.WriteString(digest); err != nil {
		return "", errors.Join(fmt.Errorf("writing fault configuration binding: %w", err), file.Close(), os.Remove(temporary))
	}
	if err := file.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("closing fault configuration binding: %w", err), os.Remove(temporary))
	}
	target := filepath.Join(namespace, "configuration")
	linkErr := os.Link(temporary, target)
	removeErr := os.Remove(temporary)
	if removeErr != nil {
		return "", fmt.Errorf("removing temporary fault binding: %w", removeErr)
	}
	if linkErr != nil && !errors.Is(linkErr, fs.ErrExist) {
		return "", fmt.Errorf("publishing fault configuration binding: %w", linkErr)
	}
	bound, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("reading fault configuration binding: %w", err)
	}
	if string(bound) != digest {
		return "", fmt.Errorf("fault configuration changed within an active private attempt")
	}
	return namespace, nil
}

func responseState(namespace string, index int) (string, error) {
	dir := filepath.Join(namespace, fmt.Sprintf("response-%08d", index))
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("creating fault response state: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("checking fault response state: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fault response state is not a directory")
	}
	return dir, nil
}

func observe(observer Observer, matcher, step int, transition Transition) error {
	if observer == nil {
		return nil
	}
	if err := observer(matcher, step, transition); err != nil {
		return fmt.Errorf("observing fault %s: %w", transition, err)
	}
	return nil
}

func observationFailure(observer Observer, matcher, step int, err error) error {
	transition := Failed
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		transition = Canceled
	}
	return errors.Join(err, observe(observer, matcher, step, transition))
}

func execute(ctx context.Context, dir string, matcher, count int, finite bool, delay func(int) int64, deliver func(int) error, observer Observer) error {
	index, observedStep, err := reserveExecution(ctx, dir, matcher, count, finite, observer)
	if err != nil {
		return err
	}
	return deliverExecution(ctx, matcher, index, observedStep, delay, deliver, observer)
}

func reserveExecution(ctx context.Context, dir string, matcher, count int, finite bool, observer Observer) (int, int, error) {
	if err := ctx.Err(); err != nil {
		return -1, -1, observationFailure(observer, matcher, -1, err)
	}
	index, observedStep := 0, -1
	if finite {
		var err error
		index, err = faultsequence.Reserve(ctx, dir, count)
		if index >= 0 {
			observedStep = index
			err = errors.Join(err, observe(observer, matcher, index, Reserved))
		}
		if err != nil {
			return index, observedStep, observationFailure(observer, matcher, observedStep, err)
		}
	}
	return index, observedStep, nil
}

func deliverExecution(ctx context.Context, matcher, index, observedStep int, delay func(int) int64, deliver func(int) error, observer Observer) error {
	if err := faultsequence.Delay(ctx, delay(index)); err != nil {
		return observationFailure(observer, matcher, observedStep, err)
	}
	if err := ctx.Err(); err != nil {
		return observationFailure(observer, matcher, observedStep, err)
	}
	if err := deliver(index); err != nil {
		return observationFailure(observer, matcher, observedStep, fmt.Errorf("delivering fault response: %w", err))
	}
	// A failed completion observer cannot undo or relabel successful delivery.
	return observe(observer, matcher, observedStep, Delivered)
}

func sourceSteps(source object) ([]object, bool, error) {
	raw, finite := source["sequence"]
	if !finite {
		return []object{source}, false, nil
	}
	var steps []json.RawMessage
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, false, fmt.Errorf("decoding validated fault steps: %w", err)
	}
	out := make([]object, 0, len(steps))
	for _, step := range steps {
		fields, _, err := decodeObject(step)
		if err != nil {
			return nil, false, err
		}
		out = append(out, fields)
	}
	return out, true, nil
}

func stepDelay(step object) (int64, error) {
	var milliseconds int64
	if raw, ok := step["delay_ms"]; ok {
		if err := json.Unmarshal(raw, &milliseconds); err != nil {
			return 0, fmt.Errorf("decoding validated fault delay: %w", err)
		}
	}
	return milliseconds, nil
}
