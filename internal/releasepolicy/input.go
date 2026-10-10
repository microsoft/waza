package releasepolicy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

// Input documents use the corresponding policy 1.0 member shape, not a
// commitment or an enforcement artifact. Required/null checks remain strict.
func decodeInput(data []byte, target any) error {
	object, err := objectJSON(data)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decoding policy planning input: %w", err)
	}
	return requireFields(object, reflect.TypeOf(target), "")
}

func DecodeDesignInput(data []byte) (Design, error) {
	var design Design
	err := decodeInput(data, &design)
	return design, err
}

func DecodeRequirementsInput(data []byte) (Requirements, error) {
	var requirements Requirements
	err := decodeInput(data, &requirements)
	return requirements, err
}
