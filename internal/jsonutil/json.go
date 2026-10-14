package jsonutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Parse preserves number tokens and rejects ambiguous or excessively nested JSON.
func Parse(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("JSON is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := read(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("JSON contains trailing data")
	}
	return value, nil
}

func read(decoder *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("JSON exceeds the supported nesting depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, errors.New("invalid JSON")
	}
	switch token {
	case json.Delim('{'):
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, errors.New("invalid JSON object key")
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid JSON object key")
			}
			if _, exists := object[name]; exists {
				return nil, errors.New("duplicate JSON object key")
			}
			object[name], err = read(decoder, depth+1)
			if err != nil {
				return nil, err
			}
		}
		if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
			return nil, errors.New("invalid JSON object")
		}
		return object, nil
	case json.Delim('['):
		array := []any{}
		for decoder.More() {
			value, err := read(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if close, err := decoder.Token(); err != nil || close != json.Delim(']') {
			return nil, errors.New("invalid JSON array")
		}
		return array, nil
	}
	return token, nil
}
