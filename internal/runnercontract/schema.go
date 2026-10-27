package runnercontract

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type schemaPair struct{ request, result *jsonschema.Schema }

var contractSchemas = sync.OnceValues(func() (schemaPair, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-request-1.0.json": schemas.RunnerRequestSchemaJSON,
		"runner-result-1.0.json":  schemas.RunnerResultSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return schemaPair{}, err
		}
		if err := c.AddResource(name, doc); err != nil {
			return schemaPair{}, err
		}
	}
	req, err := c.Compile("runner-request-1.0.json")
	if err != nil {
		return schemaPair{}, err
	}
	res, err := c.Compile("runner-result-1.0.json")
	return schemaPair{request: req, result: res}, err
})

func Parse(data []byte) (*Request, error) {
	if len(data) > MaxRequestBytes {
		return nil, fmt.Errorf("request exceeds %d bytes", MaxRequestBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, err
	}
	pair, err := contractSchemas()
	if err != nil {
		return nil, err
	}
	if err := pair.request.Validate(doc); err != nil {
		return nil, fmt.Errorf("runner request schema: %w", err)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func ValidateResult(result *Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(data)+1 > MaxResultBytes {
		return fmt.Errorf("terminal envelope exceeds %d bytes", MaxResultBytes)
	}

	doc, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	pair, err := contractSchemas()
	if err != nil {
		return err
	}
	if err := pair.result.Validate(doc); err != nil {
		return fmt.Errorf("runner result schema: %w", err)
	}
	if result.ExitCode != result.Outcome.ExitCode() {
		return fmt.Errorf("terminal outcome and exit code disagree")
	}
	if result.AdjacentWork || result.SelectionValid != (result.Resolved != nil) {
		return fmt.Errorf("inconsistent exact-unit selection")
	}
	if result.SelectionValid && (result.Requested == nil || *result.Requested != *result.Resolved || result.Identity == nil) {
		return fmt.Errorf("resolved selection must retain exact requested tuple and identity")
	}
	if result.Outcome == Success || result.Outcome == EvaluationFailure {
		run := result.Observation
		if run == nil || result.Resolved == nil || run.RunNumber != result.Resolved.Trial || run.Attempts != result.Resolved.Attempt {
			return fmt.Errorf("graded observation must bind the exact resolved trial/attempt")
		}
		if result.Outcome == Success {
			if run.Status != models.StatusPassed || run.ErrorMsg != "" || len(run.Validations) == 0 {
				return fmt.Errorf("success requires complete passing checks without execution errors")
			}
			for _, check := range run.Validations {
				if !check.Passed {
					return fmt.Errorf("success cannot contain failed checks")
				}
			}
		} else {
			failed := false
			for _, check := range run.Validations {
				failed = failed || !check.Passed
			}
			if run.Status != models.StatusFailed || run.ErrorMsg != "" || !failed {
				return fmt.Errorf("evaluation failure requires a failed check without execution errors")
			}
		}
	}
	return nil
}

// ParseResult validates original wire data before typed decoding.
func ParseResult(data []byte) (*Result, error) {
	if len(data) > MaxResultBytes {
		return nil, fmt.Errorf("terminal envelope exceeds %d bytes", MaxResultBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, err
	}
	pair, err := contractSchemas()
	if err != nil {
		return nil, err
	}
	if err := pair.result.Validate(doc); err != nil {
		return nil, fmt.Errorf("runner result schema: %w", err)
	}
	var result Result
	type envelope Result
	raw := struct {
		*envelope
		Observation json.RawMessage `json:"observation"`
	}{envelope: (*envelope)(&result)}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if len(raw.Observation) != 0 {
		result.Observation = &models.RunResult{}
		if err := result.Observation.UnmarshalJSONExact(raw.Observation); err != nil {
			return nil, err
		}
	}
	if err := ValidateResult(&result); err != nil {
		return nil, err
	}
	return &result, nil
}
