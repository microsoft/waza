package releasepolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
)

func SourceDigest(data []byte) models.EvidenceDigest {
	sum := sha256.Sum256(data)
	return models.EvidenceDigest{Encoding: "source-bytes", SHA256: hex.EncodeToString(sum[:])}
}

func validSourceDigest(d models.EvidenceDigest) bool {
	decoded, err := hex.DecodeString(d.SHA256)
	return err == nil && len(decoded) == sha256.Size && d.Encoding == "source-bytes" &&
		d.SHA256 == strings.ToLower(d.SHA256)
}

func DecodeAssuranceContract(data []byte, p *Policy) (*AssuranceContract, error) {
	var c AssuranceContract
	if err := exactAssuranceFields(data, reflect.TypeFor[AssuranceContract]()); err != nil {
		return nil, err
	}
	if _, err := admit(data, AssuranceContractKind, &c); err != nil {
		return nil, err
	}
	if p == nil || !p.Requirements.Assurance || c.PolicyDigest != p.Digest || len(c.Arms) != 2 ||
		len(c.Nonce) != 64 || c.Nonce != strings.ToLower(c.Nonce) {
		return nil, fmt.Errorf("assurance contract requires its exact assurance-selected policy, two arms and nonce")
	}
	if _, err := hex.DecodeString(c.Nonce); err != nil {
		return nil, fmt.Errorf("invalid assurance contract nonce: %w", err)
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		a, exists := c.Arms[arm]
		if !exists || a.Mode != "authored_finite_output" || a.PlanDigest != p.Arms[arm].Digest {
			return nil, fmt.Errorf("%s assurance mode or resolved plan is mismatched", arm)
		}
		for _, digest := range []models.EvidenceDigest{a.EvalSourceDigest, a.ExecutableDigest, a.LabelsDigest} {
			if !validSourceDigest(digest) {
				return nil, fmt.Errorf("%s assurance source-byte digest is invalid", arm)
			}
		}
		if !isDigest(a.ResolvedConfigDigest) || a.ResolvedConfigDigest.Encoding != "json-v1" {
			return nil, fmt.Errorf("%s assurance resolved configuration digest is invalid", arm)
		}
		if a.ReviewDigest != nil && !validSourceDigest(*a.ReviewDigest) {
			return nil, fmt.Errorf("%s assurance review digest is invalid", arm)
		}
		tasks := map[string]bool{}
		for _, task := range a.Tasks {
			if task.TaskID == "" || tasks[task.TaskID] || !isDigest(task.Digest) || task.Digest.Encoding != "json-v1" {
				return nil, fmt.Errorf("%s assurance task declaration is invalid or repeated", arm)
			}
			tasks[task.TaskID] = true
		}
		if len(tasks) != len(p.Arms[arm].Plan.Tasks) {
			return nil, fmt.Errorf("%s assurance task inventory is incomplete", arm)
		}
		for _, task := range p.Arms[arm].Plan.Tasks {
			if !tasks[task.ID] {
				return nil, fmt.Errorf("%s assurance task is missing: %s", arm, task.ID)
			}
		}
		seen := map[string]bool{}
		covered := map[string]bool{}
		for _, check := range a.Checks {
			key := check.TaskID + "\x00" + check.ValidationKey
			if !tasks[check.TaskID] || check.RequirementID == "" ||
				(check.Check.Scope != "eval" && check.Check.Scope != "task") ||
				check.Check.AfterTurn != 0 || check.Check.Grader == "" ||
				check.ValidationKey != check.Check.Grader || seen[key] ||
				!isDigest(check.Declaration) || check.Declaration.Encoding != "json-v1" {
				return nil, fmt.Errorf("%s assurance endpoint mapping is invalid or non-injective", arm)
			}
			seen[key], covered[check.TaskID] = true, true
		}
		if len(covered) != len(tasks) {
			return nil, fmt.Errorf("%s assurance endpoint mapping lacks selected tasks", arm)
		}
		last := ""
		for _, input := range a.Inputs {
			if input.Path <= last || !filepath.IsLocal(input.Path) ||
				strings.Contains(input.Path, "\\") || filepath.ToSlash(filepath.Clean(input.Path)) != input.Path ||
				!validSourceDigest(input.Digest) {
				return nil, fmt.Errorf("%s assurance input inventory is invalid or unordered", arm)
			}
			last = input.Path
		}
		if len(a.Inputs) == 0 {
			return nil, fmt.Errorf("%s assurance inputs are missing", arm)
		}
	}
	return &c, nil
}

func validateAssuranceOutput(output AssuranceOutput) error {
	switch output.Availability {
	case "available":
		if output.Value == nil || output.Reason != "" {
			return fmt.Errorf("available assurance output requires an explicit value without unavailable reason")
		}
	case "unavailable":
		if output.Value != nil || strings.TrimSpace(output.Reason) == "" {
			return fmt.Errorf("unavailable assurance output requires a reason and no manufactured value")
		}
	default:
		return fmt.Errorf("unknown assurance output availability")
	}
	return nil
}

// ReadAssuranceLedger verifies a new profile only; it never upgrades base 1.0.
func ReadAssuranceLedger(directory string, p *Policy, c *AssuranceContract) (*AssuranceLedger, map[Arm]Results, error) {
	return ReadAssuranceLedgerContext(context.Background(), directory, p, c)
}

// ReadAssuranceLedgerContext supports cancellation of fixed, bounded rooted reads.
func ReadAssuranceLedgerContext(ctx context.Context, directory string, p *Policy, c *AssuranceContract) (*AssuranceLedger, map[Arm]Results, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if p == nil || c == nil {
		return nil, nil, fmt.Errorf("assurance ledger needs an explicitly selected policy and contract")
	}
	if _, err := ReadSelectedDecisionContext(ctx, directory, p.Digest); err != nil {
		return nil, nil, fmt.Errorf("assurance ledger core collection verification: %w", err)
	}
	data, err := readArtifactContext(ctx, directory, "assurance-contract.json")
	if err != nil {
		return nil, nil, err
	}
	stored, err := DecodeAssuranceContract(data, p)
	if err != nil || stored.Digest != c.Digest {
		if err != nil {
			return nil, nil, fmt.Errorf("precollection assurance contract is invalid: %w", err)
		}
		return nil, nil, fmt.Errorf("precollection assurance contract is mismatched")
	}
	data, err = readArtifactContext(ctx, directory, "assurance-ledger.json")
	if err != nil {
		return nil, nil, err
	}
	var ledger AssuranceLedger
	if err := exactAssuranceFields(data, reflect.TypeFor[AssuranceLedger]()); err != nil {
		return nil, nil, err
	}
	if _, err := admit(data, AssuranceLedgerKind, &ledger); err != nil {
		return nil, nil, err
	}
	cid := c.Digest.SHA256
	if ledger.CollectionID != cid || ledger.ContractDigest != c.Digest {
		return nil, nil, fmt.Errorf("assurance ledger collection identity differs from its contract")
	}
	stream, err := readArtifactContext(ctx, directory, "assurance.ndjson")
	if err != nil {
		return nil, nil, err
	}
	if len(stream) == 0 || stream[len(stream)-1] != '\n' || SourceDigest(stream) != ledger.StreamDigest {
		return nil, nil, fmt.Errorf("assurance row tape is incomplete or mismatched")
	}
	lines := bytes.Split(stream[:len(stream)-1], []byte{'\n'})
	if len(lines) != len(ledger.Rows) {
		return nil, nil, fmt.Errorf("assurance row tape count mismatch")
	}
	rawStream, err := readArtifactContext(ctx, directory, "assurance-rows.ndjson")
	if err != nil {
		return nil, nil, err
	}
	if len(rawStream) == 0 || rawStream[len(rawStream)-1] != '\n' {
		return nil, nil, fmt.Errorf("assurance full-row payload tape is incomplete")
	}
	payloads := bytes.Split(rawStream[:len(rawStream)-1], []byte{'\n'})
	if len(payloads) != len(ledger.Rows) {
		return nil, nil, fmt.Errorf("assurance full-row payload tape has missing or orphan records")
	}
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := exactAssuranceFields(line, reflect.TypeFor[AssuranceRow]()); err != nil {
			return nil, nil, err
		}
		object, err := objectJSON(line)
		if err != nil {
			return nil, nil, err
		}
		var row AssuranceRow
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&row); err != nil {
			return nil, nil, err
		}
		if err := requireFields(object, reflect.TypeFor[AssuranceRow](), ""); err != nil {
			return nil, nil, err
		}
		if row.Kind != AssuranceRowKind || row.Version != AssuranceVersion || row.Sequence != i+1 ||
			row.CollectionID != cid || !reflect.DeepEqual(row, ledger.Rows[i]) {
			return nil, nil, fmt.Errorf("assurance row tape sequence or publication mismatch")
		}
		if err := validateAssuranceOutput(row.Output); err != nil {
			return nil, nil, err
		}
		payload, err := objectJSON(payloads[i])
		if err != nil {
			return nil, nil, err
		}
		digest, err := evidence.JSONDigest(payload)
		if err != nil || *digest != row.RowDigest {
			return nil, nil, fmt.Errorf("durably retained full assurance row identity mismatch")
		}
	}
	data, err = readArtifactContext(ctx, directory, "journal.json")
	if err != nil {
		return nil, nil, err
	}
	journal, err := DecodeJournal(data, p)
	if err != nil {
		return nil, nil, err
	}
	if journal.CollectionID != cid || journal.Digest != ledger.CoreJournalDigest {
		return nil, nil, fmt.Errorf("assurance ledger core journal identity mismatch")
	}
	starts, terminals := 0, 0
	for _, event := range journal.Events {
		switch event.Type {
		case "attempt_start":
			if event.Key == nil || starts >= len(ledger.Rows) || ledger.Rows[starts].Key != *event.Key {
				return nil, nil, fmt.Errorf("assurance ledger lacks exact contiguous core starts")
			}
			starts++
		case "attempt_terminal":
			if event.Attempt == nil || terminals >= len(ledger.Rows) || ledger.Rows[terminals].Key != event.Attempt.Key ||
				ledger.Rows[terminals].Origin != event.Attempt.Origin {
				return nil, nil, fmt.Errorf("assurance ledger lacks exact contiguous core terminals")
			}
			if event.Attempt.Category == "behavioral" && ledger.Rows[terminals].Output.Availability != "available" {
				return nil, nil, fmt.Errorf("behavioral attempt lacks captured output")
			}
			terminals++
		}
	}
	if starts != terminals || terminals != len(ledger.Rows) {
		return nil, nil, fmt.Errorf("assurance ledger contains missing, extra or orphan attempt rows")
	}
	results := map[Arm]Results{}
	rowIndex := map[Arm]int{}
	rawRows := map[Arm][]any{}
	if len(ledger.RawResults) != 2 {
		return nil, nil, fmt.Errorf("assurance ledger lacks both raw-result identities")
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		data, err := readArtifactContext(ctx, directory, string(arm)+".results.json")
		if err != nil {
			return nil, nil, err
		}
		if SourceDigest(data) != ledger.RawResults[arm] {
			return nil, nil, fmt.Errorf("%s exact raw result bytes mismatch", arm)
		}
		decoded, err := DecodeResults(data)
		if err != nil {
			return nil, nil, err
		}
		if decoded.CollectionID != cid {
			return nil, nil, fmt.Errorf("%s raw result collection identity mismatch", arm)
		}
		results[arm] = *decoded
		object, err := objectJSON(data)
		if err != nil {
			return nil, nil, err
		}
		array, ok := object["rows"].([]any)
		if !ok || len(array) != len(decoded.Rows) {
			return nil, nil, fmt.Errorf("%s raw result row inventory is unavailable", arm)
		}
		rawRows[arm] = array
	}
	for _, row := range ledger.Rows {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		index := rowIndex[row.Key.Arm]
		array := rawRows[row.Key.Arm]
		if index >= len(array) {
			return nil, nil, fmt.Errorf("assurance ledger has no corresponding actual row")
		}
		digest, err := evidence.JSONDigest(array[index])
		if err != nil || *digest != row.RowDigest {
			return nil, nil, fmt.Errorf("full assurance row JSON-v1 identity mismatch")
		}
		actual := results[row.Key.Arm].Rows[index]
		if actual.Origin != row.Origin {
			return nil, nil, fmt.Errorf("assurance row origin mismatch")
		}
		if row.Output.Availability == "available" {
			object, ok := array[index].(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("actual preserved row is not an object")
			}
			run, ok := object["run"].(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("actual run output object is unavailable")
			}
			output, ok := run["final_output"].(string)
			if !ok || output != *row.Output.Value {
				return nil, nil, fmt.Errorf("actual preserved output is absent or mismatched")
			}
		}
		rowIndex[row.Key.Arm]++
	}
	for _, arm := range []Arm{Baseline, Candidate} {
		if rowIndex[arm] != len(results[arm].Rows) {
			return nil, nil, fmt.Errorf("assurance ledger omits actual result rows")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return &ledger, results, nil
}

// NonAssurancePass keeps the base required-assurance decision unchanged.
func (d Decision) NonAssurancePass() bool {
	return d.Compatibility.State == "compatible" && d.Completeness.State == "complete" &&
		d.Operations.State == "observed" &&
		(d.Golden.State == "passed" || d.Golden.State == "not_required") &&
		(d.Billing.State == "within_budget" || d.Billing.State == "not_required") &&
		(d.Statistics.State == "noninferiority" || d.Statistics.State == "improvement")
}

func exactAssuranceFields(data []byte, typ reflect.Type) error {
	object, err := objectJSON(data)
	if err != nil {
		return err
	}
	return exactAssuranceNames(object, typ)
}

func exactAssuranceNames(value any, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("assurance contract object has an invalid type")
		}
		fields := map[string]reflect.Type{}
		assuranceJSONFields(typ, fields)
		for name, child := range object {
			field, exists := fields[name]
			if !exists {
				return fmt.Errorf("assurance field %q is unknown or an alias", name)
			}
			if err := exactAssuranceNames(child, field); err != nil {
				return err
			}
		}
	case reflect.Map:
		if object, ok := value.(map[string]any); ok {
			for _, child := range object {
				if err := exactAssuranceNames(child, typ.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Slice:
		if array, ok := value.([]any); ok {
			for _, child := range array {
				if err := exactAssuranceNames(child, typ.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func assuranceJSONFields(typ reflect.Type, fields map[string]reflect.Type) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if field.Anonymous && name == "" {
			assuranceJSONFields(field.Type, fields)
		} else if name != "" && name != "-" {
			fields[name] = field.Type
		}
	}
}
