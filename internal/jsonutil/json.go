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
	// Small documents benefit from avoiding Token's temporary scalar lookahead
	// errors. Large, string-heavy payloads use the original single grammar pass.
	if len(data) <= 64<<10 && json.Valid(data) {
		scan := keyScanner{data: data}
		if err := scan.value(0); err != nil {
			return nil, err
		}
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	}
	value, err := read(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("JSON contains trailing data")
	}
	return value, nil
}

// json.Valid checks grammar before this walk. Key uniqueness and the depth
// fence are checked before decoding constructs the detached value tree.
type keyScanner struct {
	data []byte
	pos  int
}

func (scan *keyScanner) whitespace() {
	scan.pos = len(scan.data) - len(bytes.TrimLeft(scan.data[scan.pos:], " \t\r\n"))
}

func (scan *keyScanner) stringBytes() []byte {
	start := scan.pos
	scan.pos++
	for {
		end := scan.pos + bytes.IndexByte(scan.data[scan.pos:], '"')
		escaped := false
		for i := end - 1; scan.data[i] == '\\'; i-- {
			escaped = !escaped
		}
		scan.pos = end + 1
		if !escaped {
			return scan.data[start:scan.pos]
		}
	}
}

type objectKeys struct {
	plain   [8][]byte
	count   int
	decoded map[string]bool
}

func (keys *objectKeys) add(raw []byte) error {
	escaped := bytes.IndexByte(raw, '\\') >= 0
	if keys.decoded == nil && !escaped && keys.count < len(keys.plain) {
		name := raw[1 : len(raw)-1]
		for _, previous := range keys.plain[:keys.count] {
			if bytes.Equal(previous, name) {
				return errors.New("duplicate JSON object key")
			}
		}
		keys.plain[keys.count] = name
		keys.count++
		return nil
	}
	// Keep comparisons bounded and decode escaped aliases exactly as the token
	// parser does. Small ordinary objects need not allocate copies of their keys.
	if keys.decoded == nil {
		keys.decoded = make(map[string]bool, len(keys.plain)+1)
		for _, previous := range keys.plain[:keys.count] {
			keys.decoded[string(previous)] = true
		}
	}
	var name string
	if escaped {
		if err := json.Unmarshal(raw, &name); err != nil {
			return errors.New("invalid JSON object key")
		}
	} else {
		name = string(raw[1 : len(raw)-1])
	}
	if keys.decoded[name] {
		return errors.New("duplicate JSON object key")
	}
	keys.decoded[name] = true
	return nil
}

func (scan *keyScanner) value(depth int) error {
	if depth > 64 {
		return errors.New("JSON exceeds the supported nesting depth")
	}
	scan.whitespace()
	switch scan.data[scan.pos] {
	case '{':
		scan.pos++
		scan.whitespace()
		var keys objectKeys
		for scan.data[scan.pos] != '}' {
			if err := keys.add(scan.stringBytes()); err != nil {
				return err
			}
			scan.whitespace()
			scan.pos++ // colon, already checked by json.Valid
			if err := scan.value(depth + 1); err != nil {
				return err
			}
			scan.whitespace()
			if scan.data[scan.pos] == ',' {
				scan.pos++
				scan.whitespace()
			}
		}
		scan.pos++
	case '[':
		scan.pos++
		scan.whitespace()
		for scan.data[scan.pos] != ']' {
			if err := scan.value(depth + 1); err != nil {
				return err
			}
			scan.whitespace()
			if scan.data[scan.pos] == ',' {
				scan.pos++
			}
		}
		scan.pos++
	case '"':
		scan.stringBytes()
	default:
		end := bytes.IndexAny(scan.data[scan.pos:], " \t\r\n,]}")
		if end < 0 {
			scan.pos = len(scan.data)
		} else {
			scan.pos += end
		}
	}
	return nil
}

// ValidateUnicodeEscapes rejects escapes that encoding/json would replace with
// U+FFFD. It does not replace JSON syntax validation.
func ValidateUnicodeEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		next := bytes.IndexByte(data[i:], '\\')
		if next < 0 {
			break
		}
		i += next
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
