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
	if err := ValidateUnicodeEscapes(data); err != nil {
		return nil, err
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

// ValidateUnicodeEscapes rejects escapes that encoding/json would replace with
// U+FFFD. It does not replace JSON syntax validation.
func ValidateUnicodeEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return errors.New("JSON contains a truncated escape")
		}
		if data[i] != 'u' {
			continue
		}
		code, ok := unicodeEscapeCode(data[i+1:])
		if !ok {
			return errors.New("JSON contains an invalid Unicode escape")
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errors.New("JSON contains an unpaired Unicode surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
				return errors.New("JSON contains an unpaired Unicode surrogate")
			}
			low, ok := unicodeEscapeCode(data[i+3:])
			if !ok || low < 0xdc00 || low > 0xdfff {
				return errors.New("JSON contains an unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

func unicodeEscapeCode(data []byte) (uint16, bool) {
	if len(data) < 4 {
		return 0, false
	}
	var code uint16
	for _, digit := range data[:4] {
		code <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			code += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			code += uint16(digit - 'a' + 10)
		case digit >= 'A' && digit <= 'F':
			code += uint16(digit - 'A' + 10)
		default:
			return 0, false
		}
	}
	return code, true
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
