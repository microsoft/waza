package assurance

import (
	"bytes"
	"encoding"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
)

const (
	qualificationVersion         = "1.0"
	qualificationDocumentLimit   = 16 << 20
	qualificationExecutableLimit = 128 << 20
	qualificationTotalLimit      = 256 << 20
	qualificationEncodedLimit    = 512 << 20
	qualificationSourceLimit     = 1024
	qualificationArtifactLimit   = 32768
	qualificationSafeCounter     = uint64(9007199254740991)
)

// These private handles do not establish authority, durability or qualification.
type qualificationDocument struct{ canonical string }

func (document qualificationDocument) bytes() []byte  { return []byte(document.canonical) }
func (document qualificationDocument) sha256() string { return byteSHA256(document.bytes()) }

func qualificationCanonical(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > qualificationEncodedLimit {
		return nil, errors.New("qualification: JSON byte limit")
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("qualification: strict JSON: %w", err)
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func qualificationSeal(value any) (qualificationDocument, error) {
	return qualificationSealBounded(value, qualificationDocumentLimit, nil)
}

func qualificationSealBounded(value any, limit int, materializing func()) (qualificationDocument, error) {
	data, err := qualificationMarshalBounded(value, limit, materializing)
	if err != nil {
		return qualificationDocument{}, err
	}
	canonical, err := qualificationCanonical(data)
	if err != nil {
		return qualificationDocument{}, err
	}
	if len(canonical) > limit {
		return qualificationDocument{}, errors.New("qualification: canonical projection byte limit")
	}
	return qualificationDocument{canonical: string(canonical)}, nil
}

func qualificationMarshalBounded(value any, limit int, materializing func()) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("qualification: explicit projection budget required")
	}
	budget := limit
	if err := qualificationProjectionSize(reflect.ValueOf(value), &budget, 0); err != nil {
		return nil, err
	}
	if materializing != nil {
		materializing()
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > limit {
		return nil, errors.Join(errors.New("qualification: projection encoding invalid or oversized"), err)
	}
	return data, nil
}

func qualificationSpendProjection(budget *int, size int) error {
	if size < 0 || size > *budget {
		return errors.New("qualification: projection byte limit before serialization")
	}
	*budget -= size
	return nil
}

func qualificationRuneSize(character rune) int {
	switch character {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return 2
	case '<', '>', '&', '\u2028', '\u2029':
		return 6
	}
	if character < 0x20 {
		return 6
	}
	return utf8.RuneLen(character)
}

func qualificationStringSize(value string, budget *int) error {
	if len(value) > *budget || !utf8.ValidString(value) {
		return errors.New("qualification: projection string byte limit or Unicode")
	}
	if err := qualificationSpendProjection(budget, 2); err != nil {
		return err
	}
	for _, character := range value {
		if err := qualificationSpendProjection(budget, qualificationRuneSize(character)); err != nil {
			return err
		}
	}
	return nil
}

func qualificationRawSize(data json.RawMessage, budget *int) error {
	if len(data) > *budget || !utf8.Valid(data) || !json.Valid(data) {
		return errors.New("qualification: bounded raw projection required")
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return err
	}
	for i := 0; i < len(data); {
		if strings.ContainsRune(" \r\n\t", rune(data[i])) {
			i++
			continue
		}
		if data[i] != '"' {
			if err := qualificationSpendProjection(budget, 1); err != nil {
				return err
			}
			i++
			continue
		}
		i++
		if err := qualificationSpendProjection(budget, 2); err != nil {
			return err
		}
		for data[i] != '"' {
			character, width := utf8.DecodeRune(data[i:])
			i += width
			if character == '\\' {
				size := 2
				if data[i] == 'u' {
					size = 6
					i += 4
				}
				i++
				if err := qualificationSpendProjection(budget, size); err != nil {
					return err
				}
				continue
			}
			if err := qualificationSpendProjection(budget, qualificationRuneSize(character)); err != nil {
				return err
			}
		}
		i++
	}
	return nil
}

// Only ordinary concrete native JSON shapes are supported. Unknown custom
// marshalers fail explicitly rather than running unbounded user code to size it.
func qualificationProjectionSize(value reflect.Value, budget *int, depth int) error {
	if depth > 64 {
		return errors.New("qualification: projection nesting limit")
	}
	if !value.IsValid() {
		return qualificationSpendProjection(budget, 4)
	}
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) && value.IsNil() {
		return qualificationSpendProjection(budget, 4)
	}
	if value.Type() == reflect.TypeFor[*json.RawMessage]() {
		return qualificationProjectionSize(value.Elem(), budget, depth+1)
	}
	if value.Type() == reflect.TypeFor[json.RawMessage]() {
		if value.IsNil() {
			return qualificationSpendProjection(budget, 4)
		}
		return qualificationRawSize(value.Bytes(), budget)
	}
	if value.Type() == reflect.TypeFor[json.Number]() {
		number := value.String()
		if len(number) > *budget || !json.Valid([]byte(number)) || len(number) == 0 || !strings.ContainsRune("-0123456789", rune(number[0])) {
			return errors.New("qualification: bounded JSON number required")
		}
		return qualificationSpendProjection(budget, len(number))
	}
	custom := func(kind reflect.Type) bool {
		return kind.Implements(reflect.TypeFor[json.Marshaler]()) || kind.Implements(reflect.TypeFor[encoding.TextMarshaler]())
	}
	if custom(value.Type()) || (value.CanAddr() && custom(value.Addr().Type())) {
		return errors.New("qualification: custom native marshaler integration unsupported")
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return qualificationSpendProjection(budget, 4)
		}
		return qualificationProjectionSize(value.Elem(), budget, depth+1)
	case reflect.String:
		return qualificationStringSize(value.String(), budget)
	case reflect.Bool:
		size := 5
		if value.Bool() {
			size = 4
		}
		return qualificationSpendProjection(budget, size)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return qualificationSpendProjection(budget, len(strconv.FormatInt(value.Int(), 10)))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return qualificationSpendProjection(budget, len(strconv.FormatUint(value.Uint(), 10)))
	case reflect.Float32, reflect.Float64:
		data, err := json.Marshal(value.Interface())
		if err != nil {
			return err
		}
		return qualificationSpendProjection(budget, len(data))
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return qualificationSpendProjection(budget, 4)
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return errors.New("qualification: implicit binary projection unsupported")
		}
		if err := qualificationSpendProjection(budget, 2+max(0, value.Len()-1)); err != nil {
			return err
		}
		for i := range value.Len() {
			if err := qualificationProjectionSize(value.Index(i), budget, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return qualificationSpendProjection(budget, 4)
		}
		if value.Type().Key().Kind() != reflect.String {
			return errors.New("qualification: string map keys required")
		}
		if err := qualificationSpendProjection(budget, 2+max(0, value.Len()-1)); err != nil {
			return err
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := qualificationStringSize(iterator.Key().String(), budget); err != nil {
				return err
			}
			if err := qualificationSpendProjection(budget, 1); err != nil {
				return err
			}
			if err := qualificationProjectionSize(iterator.Value(), budget, depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := qualificationSpendProjection(budget, 2); err != nil {
			return err
		}
		seen := map[string]bool{}
		var fields func(reflect.Value) error
		fields = func(object reflect.Value) error {
			for i := range object.NumField() {
				field := object.Type().Field(i)
				if field.PkgPath != "" && (!field.Anonymous || field.Type.Kind() != reflect.Struct) {
					continue
				}
				name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
				if name == "-" {
					continue
				}
				item := object.Field(i)
				if field.Anonymous && name == "" && item.Kind() == reflect.Pointer {
					return errors.New("qualification: anonymous native pointer integration unsupported")
				}
				if field.Anonymous && name == "" && item.Kind() == reflect.Struct {
					if err := fields(item); err != nil {
						return err
					}
					continue
				}
				if strings.Contains(options, "omitempty") && qualificationJSONEmpty(item) {
					continue
				}
				if options != "" && options != "omitempty" {
					return errors.New("qualification: native JSON tag option unsupported")
				}
				if name == "" {
					name = field.Name
				}
				if seen[name] {
					return errors.New("qualification: ambiguous native JSON fields")
				}
				punctuation := 1
				if len(seen) > 0 {
					punctuation++
				}
				seen[name] = true
				if err := qualificationStringSize(name, budget); err != nil {
					return err
				}
				if err := qualificationSpendProjection(budget, punctuation); err != nil {
					return err
				}
				if err := qualificationProjectionSize(item, budget, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		return fields(value)
	default:
		return errors.New("qualification: native JSON projection shape unsupported")
	}
}

func qualificationJSONEmpty(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return value.Len() == 0
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Interface, reflect.Pointer:
		return value.IsZero()
	}
	return false
}

// Exact field spelling/requiredness and scalar token types are checked before
// decoding: encoding/json otherwise accepts null scalars and case aliases.
func qualificationDecode[T any](data []byte, limit int) (T, error) {
	var target T
	if limit < 1 || len(data) == 0 || len(data) > limit {
		return target, errors.New("qualification: document byte limit")
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return target, err
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return target, err
	}
	if err := qualificationShape(value, reflect.TypeFor[T]()); err != nil {
		return target, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&target); err != nil {
		return target, fmt.Errorf("qualification: decode fields: %w", err)
	}
	return target, nil
}

func qualificationShape(value any, kind reflect.Type) error {
	if kind == reflect.TypeFor[json.RawMessage]() {
		if value == nil {
			return errors.New("qualification: native projection cannot be null")
		}
		return nil
	}
	if kind.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return qualificationShape(value, kind.Elem())
	}
	switch kind.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok || len(object) != kind.NumField() {
			return errors.New("qualification: required object fields")
		}
		for index := range kind.NumField() {
			field := kind.Field(index)
			key := field.Tag.Get("json")
			if key == "" || strings.Contains(key, ",") {
				return errors.New("qualification: unsupported private projection")
			}
			item, present := object[key]
			if !present {
				return errors.New("qualification: missing exact field")
			}
			if err := qualificationShape(item, field.Type); err != nil {
				return fmt.Errorf("qualification: field %s: %w", key, err)
			}
		}
	case reflect.Slice:
		array, ok := value.([]any)
		if !ok || len(array) > qualificationArtifactLimit {
			return errors.New("qualification: required bounded array")
		}
		for _, item := range array {
			if err := qualificationShape(item, kind.Elem()); err != nil {
				return err
			}
		}
	case reflect.String:
		if _, ok := value.(string); !ok {
			return errors.New("qualification: string token required")
		}
	case reflect.Bool:
		if _, ok := value.(bool); !ok {
			return errors.New("qualification: boolean token required")
		}
	case reflect.Int, reflect.Int64, reflect.Uint64:
		number, ok := value.(json.Number)
		if !ok {
			return errors.New("qualification: integer token required")
		}
		count, err := strconv.ParseUint(string(number), 10, 64)
		if err != nil || count > qualificationSafeCounter {
			return errors.New("qualification: owner integer range")
		}
	default:
		return errors.New("qualification: unsupported private field type")
	}
	return nil
}

func qualificationIdentifier(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func qualificationDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

// Preflight every encoded item before any decoded allocation, including
// repeated items; declared length never determines an allocation.
func qualificationBase64Size(value string, limit uint64) (uint64, error) {
	if len(value)%4 != 0 {
		return 0, errors.New("qualification: padded base64 required")
	}
	padding := 0
	if strings.HasSuffix(value, "=") {
		padding++
	}
	if strings.HasSuffix(value, "==") {
		padding++
	}
	size := uint64(len(value)/4)*3 - uint64(padding)
	if size > limit {
		return 0, errors.New("qualification: decoded byte limit")
	}
	for i := range len(value) - padding {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/", rune(value[i])) {
			return 0, errors.New("qualification: canonical base64 alphabet required")
		}
	}
	// Strict trailing-bit validation without allocating decoded bytes.
	if padding > 0 {
		alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
		last := strings.IndexByte(alphabet, value[len(value)-padding-1])
		mask := 3
		if padding == 2 {
			mask = 15
		}
		if last < 0 || last&mask != 0 {
			return 0, errors.New("qualification: noncanonical base64 trailing bits")
		}
	}
	return size, nil
}

type qualificationCheck struct {
	Scope     string `json:"scope"`
	AfterTurn int    `json:"after_turn"`
	Grader    string `json:"grader"`
}

type qualificationSelector struct {
	ArmID         string             `json:"arm_id"`
	TaskID        string             `json:"task_id"`
	RequirementID string             `json:"requirement_id"`
	Check         qualificationCheck `json:"check"`
	CaseID        string             `json:"case_id"`
}

func (selector qualificationSelector) valid() bool {
	return qualificationIdentifier(selector.ArmID) && qualificationIdentifier(selector.TaskID) &&
		qualificationIdentifier(selector.RequirementID) && qualificationIdentifier(selector.CaseID) &&
		qualificationIdentifier(selector.Check.Grader) && selector.Check.AfterTurn == 0 &&
		(selector.Check.Scope == "task" || selector.Check.Scope == "eval")
}

type qualificationArtifact struct {
	Role        string  `json:"role"`
	Ordinal     *uint64 `json:"ordinal"`
	Encoding    string  `json:"encoding"`
	ByteLength  uint64  `json:"byte_length"`
	SHA256      string  `json:"sha256"`
	BytesBase64 string  `json:"bytes_base64"`
}

type qualificationEncodedClaim struct {
	value         string
	length, limit uint64
}

func qualificationPreflightEncoded(claims []qualificationEncodedClaim, aggregateLimit uint64) error {
	total := uint64(0)
	for _, claim := range claims {
		size, err := qualificationBase64Size(claim.value, claim.limit)
		if err != nil || size != claim.length || size > aggregateLimit-total {
			return errors.New("qualification: encoded item length or aggregate limit")
		}
		total += size
	}
	return nil
}

type qualificationBlobBounds struct {
	document, count int
	total           uint64
	role            func(string) uint64
}

type qualificationJSONSpan struct{ start, end int }

// This scan retains only offsets and bounded keys, not JSON trees or blob
// strings. json.Valid checks grammar without decoding strings or numbers.
type qualificationBlobScanner struct {
	data []byte
	pos  int
}

func (scan *qualificationBlobScanner) whitespace() {
	for scan.pos < len(scan.data) && strings.ContainsRune(" \r\n\t", rune(scan.data[scan.pos])) {
		scan.pos++
	}
}

func (scan *qualificationBlobScanner) stringSpan() qualificationJSONSpan {
	start := scan.pos
	scan.pos++
	for {
		character := scan.data[scan.pos]
		scan.pos++
		switch character {
		case '\\':
			scan.pos++
		case '"':
			return qualificationJSONSpan{start, scan.pos}
		}
	}
}

func (scan *qualificationBlobScanner) smallString(span qualificationJSONSpan) (string, error) {
	if span.end-span.start > 1538 || scan.data[span.start] != '"' {
		return "", errors.New("qualification: bounded structural string required")
	}
	var value string
	if err := json.Unmarshal(scan.data[span.start:span.end], &value); err != nil || len(value) > 256 {
		return "", errors.New("qualification: bounded structural string required")
	}
	return value, nil
}

func (scan *qualificationBlobScanner) value(depth int, fields map[string]qualificationJSONSpan) (qualificationJSONSpan, error) {
	scan.whitespace()
	start := scan.pos
	if depth > 64 {
		return qualificationJSONSpan{}, errors.New("qualification: structural nesting limit")
	}
	switch scan.data[scan.pos] {
	case '"':
		span := scan.stringSpan()
		if span.end-span.start > 1538 {
			return qualificationJSONSpan{}, errors.New("qualification: bounded structural string required")
		}
		return span, nil
	case '{':
		scan.pos++
		scan.whitespace()
		seen := map[string]bool{}
		for scan.data[scan.pos] != '}' {
			key, err := scan.smallString(scan.stringSpan())
			if err != nil || seen[key] || len(seen) >= 64 {
				return qualificationJSONSpan{}, errors.New("qualification: duplicate or unbounded structural key")
			}
			seen[key] = true
			scan.whitespace()
			scan.pos++ // colon, already validated
			scan.whitespace()
			var span qualificationJSONSpan
			if key == "bytes_base64" && fields != nil && scan.data[scan.pos] == '"' {
				span = scan.stringSpan()
			} else {
				span, err = scan.value(depth+1, nil)
			}
			if err != nil {
				return qualificationJSONSpan{}, err
			}
			if fields != nil {
				fields[key] = span
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
		count := 0
		for scan.data[scan.pos] != ']' {
			count++
			if count > qualificationArtifactLimit {
				return qualificationJSONSpan{}, errors.New("qualification: structural array limit")
			}
			if _, err := scan.value(depth+1, nil); err != nil {
				return qualificationJSONSpan{}, err
			}
			scan.whitespace()
			if scan.data[scan.pos] == ',' {
				scan.pos++
				scan.whitespace()
			}
		}
		scan.pos++
	default:
		for scan.pos < len(scan.data) && !strings.ContainsRune(" \r\n\t,]}", rune(scan.data[scan.pos])) {
			scan.pos++
		}
		if scan.pos-start > 20 {
			return qualificationJSONSpan{}, errors.New("qualification: bounded structural scalar required")
		}
	}
	return qualificationJSONSpan{start, scan.pos}, nil
}

// Count decoded JSON string characters and base64 bytes without materializing
// either string. Escaped ASCII spellings retain the existing JSON admission.
func qualificationBase64Span(data []byte, span qualificationJSONSpan, limit uint64) (uint64, error) {
	if span.end-span.start < 2 || data[span.start] != '"' {
		return 0, errors.New("qualification: base64 string required")
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var count uint64
	padding, last := 0, 0
	for i := span.start + 1; i < span.end-1; i++ {
		character := data[i]
		if character == '\\' {
			i++
			character = data[i]
			if character == 'u' {
				value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
				if err != nil || value > 127 {
					return 0, errors.New("qualification: canonical base64 alphabet required")
				}
				character = byte(value)
				i += 4
			} else if character != '/' {
				return 0, errors.New("qualification: canonical base64 alphabet required")
			}
		}
		count++
		if count > 4*((limit+2)/3) {
			return 0, errors.New("qualification: encoded role byte limit")
		}
		if character == '=' {
			padding++
			if padding > 2 {
				return 0, errors.New("qualification: canonical base64 padding required")
			}
		} else {
			last = strings.IndexByte(alphabet, character)
			if padding != 0 || last < 0 {
				return 0, errors.New("qualification: canonical base64 alphabet required")
			}
		}
	}
	if count%4 != 0 || (padding != 0 && count == 0) {
		return 0, errors.New("qualification: padded base64 required")
	}
	size := count / 4 * 3
	if size < uint64(padding) {
		return 0, errors.New("qualification: canonical base64 padding required")
	}
	size -= uint64(padding)
	if size > limit || (padding == 1 && last&3 != 0) || (padding == 2 && last&15 != 0) {
		return 0, errors.New("qualification: decoded role byte limit or trailing bits")
	}
	return size, nil
}

func qualificationPreflightBlobJSON(data []byte, sources bool, bounds qualificationBlobBounds) error {
	if len(data) == 0 || len(data) > bounds.document || !utf8.Valid(data) || !json.Valid(data) {
		return errors.New("qualification: bounded strict JSON required")
	}
	if err := validateUnicodeEscapes(data); err != nil {
		return err
	}
	scan := qualificationBlobScanner{data: data}
	scan.whitespace()
	if sources {
		if data[scan.pos] != '{' {
			return errors.New("qualification: source object required")
		}
		scan.pos++
		scan.whitespace()
		seen := map[string]bool{}
		found := false
		for data[scan.pos] != '}' {
			key, err := scan.smallString(scan.stringSpan())
			if err != nil || seen[key] || (key != "kind" && key != "version" && key != "sources") {
				return errors.New("qualification: source envelope fields")
			}
			seen[key] = true
			scan.whitespace()
			scan.pos++
			scan.whitespace()
			if key == "sources" {
				found = true
				if err := qualificationScanBlobArray(&scan, true, bounds); err != nil {
					return err
				}
			} else if _, err := scan.value(1, nil); err != nil {
				return err
			}
			scan.whitespace()
			if data[scan.pos] == ',' {
				scan.pos++
				scan.whitespace()
			}
		}
		if !found {
			return errors.New("qualification: source array required")
		}
		return nil
	}
	return qualificationScanBlobArray(&scan, false, bounds)
}

func qualificationScanBlobArray(scan *qualificationBlobScanner, sources bool, bounds qualificationBlobBounds) error {
	if scan.data[scan.pos] != '[' {
		return errors.New("qualification: blob array required")
	}
	scan.pos++
	scan.whitespace()
	total, count := uint64(0), 0
	for scan.data[scan.pos] != ']' {
		count++
		if count > bounds.count || scan.data[scan.pos] != '{' {
			return errors.New("qualification: blob object count")
		}
		fields := map[string]qualificationJSONSpan{}
		if _, err := scan.value(1, fields); err != nil {
			return err
		}
		roleSpan, rolePresent := fields["role"]
		blob, blobPresent := fields["bytes_base64"]
		length, lengthPresent := fields["byte_length"]
		if !rolePresent || !blobPresent || !lengthPresent {
			return errors.New("qualification: required blob fields")
		}
		role, err := scan.smallString(roleSpan)
		if err != nil {
			return err
		}
		switch role {
		case "eval", "task", "references", "review", "implementation_executable", "authored_input", "rubric":
			if !sources {
				return errors.New("qualification: artifact role")
			}
		case "actual_calibration_report", "core_journal", "ordered_job_tape", "job_terminal_payload":
			if sources {
				return errors.New("qualification: source role")
			}
		default:
			return errors.New("qualification: unsupported blob role")
		}
		if length.end-length.start > 20 {
			return errors.New("qualification: blob length token")
		}
		declared, err := strconv.ParseUint(string(scan.data[length.start:length.end]), 10, 64)
		if err != nil || declared > qualificationSafeCounter {
			return errors.New("qualification: blob length token")
		}
		size, err := qualificationBase64Span(scan.data, blob, min(bounds.role(role), bounds.total-total))
		if err != nil || size != declared {
			return errors.New("qualification: blob role/aggregate/declared limit")
		}
		total += size
		scan.whitespace()
		if scan.data[scan.pos] == ',' {
			scan.pos++
			scan.whitespace()
		}
	}
	scan.pos++
	return nil
}

func qualificationParseArtifacts(data []byte) ([]qualificationArtifact, error) {
	return qualificationParseArtifactsBounded(data, qualificationBlobBounds{
		document: qualificationEncodedLimit, count: qualificationArtifactLimit, total: qualificationTotalLimit,
		role: func(string) uint64 { return qualificationDocumentLimit },
	}, nil)
}

func qualificationParseArtifactsBounded(data []byte, bounds qualificationBlobBounds, materializing func()) ([]qualificationArtifact, error) {
	if err := qualificationPreflightBlobJSON(data, false, bounds); err != nil {
		return nil, err
	}
	if materializing != nil {
		materializing()
	}
	artifacts, err := qualificationDecode[[]qualificationArtifact](data, qualificationEncodedLimit)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	claims := make([]qualificationEncodedClaim, 0, len(artifacts))
	for _, artifact := range artifacts {
		limit := uint64(qualificationDocumentLimit)
		if err := qualificationArtifactMetadata(artifact); err != nil {
			return nil, err
		}
		key := artifact.Role
		if artifact.Ordinal != nil {
			key += ":" + strconv.FormatUint(*artifact.Ordinal, 10)
		}
		if seen[key] {
			return nil, errors.New("qualification: duplicate artifact key")
		}
		seen[key] = true
		claims = append(claims, qualificationEncodedClaim{artifact.BytesBase64, artifact.ByteLength, limit})
	}
	if err := qualificationPreflightEncoded(claims, qualificationTotalLimit); err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if _, err := qualificationValidateArtifact(artifact, qualificationDocumentLimit, nil); err != nil {
			return nil, err
		}
	}
	return artifacts, nil
}

func qualificationArtifactMetadata(artifact qualificationArtifact) error {
	if !qualificationDigest(artifact.SHA256) || artifact.ByteLength > qualificationSafeCounter {
		return errors.New("qualification: invalid artifact digest/length")
	}
	switch artifact.Role {
	case "actual_calibration_report":
		if artifact.Ordinal != nil || artifact.Encoding != "source-bytes-sha256" {
			return errors.New("qualification: original report role")
		}
	case "core_journal", "ordered_job_tape":
		if artifact.Ordinal != nil || artifact.Encoding != "json-v1" {
			return errors.New("qualification: canonical prefix role")
		}
	case "job_terminal_payload":
		if artifact.Ordinal == nil || *artifact.Ordinal >= 4096 || artifact.Encoding != "json-v1" {
			return errors.New("qualification: terminal role")
		}
	default:
		return errors.New("qualification: unsupported artifact role")
	}
	return nil
}

// Direct concrete validation: no artifact envelope is serialized. Metadata and
// encoded size are admitted before any decoded/canonical payload allocation.
func qualificationValidateArtifact(artifact qualificationArtifact, limit uint64, materializing func()) ([]byte, error) {
	if err := qualificationArtifactMetadata(artifact); err != nil {
		return nil, err
	}
	size, err := qualificationBase64Size(artifact.BytesBase64, limit)
	if err != nil || size != artifact.ByteLength {
		return nil, errors.New("qualification: artifact encoded role/declared limit")
	}
	if materializing != nil {
		materializing()
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(artifact.BytesBase64)
	if err != nil || byteSHA256(decoded) != artifact.SHA256 {
		return nil, errors.New("qualification: artifact original-byte digest mismatch")
	}
	if artifact.Encoding == "json-v1" {
		canonical, err := qualificationCanonical(decoded)
		if err != nil || !bytes.Equal(decoded, canonical) {
			return nil, errors.New("qualification: artifact bytes are not canonical")
		}
	}
	return decoded, nil
}

// This verifies an exact supplied role projection, not journal durability,
// cutoff semantics, global reservation, or any future qualification report.
func qualificationVerifyArtifactProjection(artifact qualificationArtifact, projection any) error {
	return qualificationVerifyArtifactProjectionBounded(artifact, projection, qualificationDocumentLimit, nil)
}

func qualificationVerifyArtifactProjectionBounded(artifact qualificationArtifact, projection any, limit uint64, materializing func()) error {
	if artifact.Encoding != "json-v1" {
		return errors.New("qualification: canonical role required")
	}
	actual, err := qualificationValidateArtifact(artifact, limit, materializing)
	if err != nil {
		return err
	}
	expected, err := qualificationSeal(projection)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected.bytes()) {
		return errors.New("qualification: artifact differs from role projection")
	}
	return nil
}

type qualificationAcknowledgment struct {
	Kind                 string  `json:"kind"`
	Version              string  `json:"version"`
	InvocationID         string  `json:"invocation_id"`
	ManifestSHA256       string  `json:"manifest_sha256"`
	BackendProfileSHA256 string  `json:"backend_profile_sha256"`
	Sequence             uint64  `json:"sequence"`
	EventSHA256          string  `json:"event_sha256"`
	PreviousSHA256       *string `json:"previous_sha256"`
	PayloadSHA256        *string `json:"payload_sha256"`
	ReceiptToken         string  `json:"receipt_token"`
}

// Syntactic admission only; no CAS, reservation or durability claim.
func qualificationParseAcknowledgment(data []byte) (qualificationDocument, error) {
	ack, err := qualificationDecode[qualificationAcknowledgment](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	if ack.Kind != "waza.qualification-acknowledgment" || ack.Version != qualificationVersion ||
		!qualificationIdentifier(ack.InvocationID) || !qualificationIdentifier(ack.ReceiptToken) ||
		!qualificationDigest(ack.ManifestSHA256) || !qualificationDigest(ack.BackendProfileSHA256) ||
		!qualificationDigest(ack.EventSHA256) ||
		(ack.PreviousSHA256 != nil && !qualificationDigest(*ack.PreviousSHA256)) ||
		(ack.PayloadSHA256 != nil && !qualificationDigest(*ack.PayloadSHA256)) ||
		(ack.Sequence == 0 && (ack.PreviousSHA256 != nil || ack.PayloadSHA256 != nil || ack.EventSHA256 != ack.ManifestSHA256)) ||
		(ack.Sequence > 0 && ack.PreviousSHA256 == nil) {
		return qualificationDocument{}, errors.New("qualification: acknowledgment shape")
	}
	return qualificationSeal(ack)
}

type qualificationCurrentnessAcknowledgment struct {
	Kind                      string `json:"kind"`
	Version                   string `json:"version"`
	RequestSHA256             string `json:"request_sha256"`
	CurrentnessProfileSHA256  string `json:"currentness_profile_sha256"`
	Stage                     string `json:"stage"`
	Current                   bool   `json:"current"`
	AssociationValid          bool   `json:"association_valid"`
	CheckedAt                 string `json:"checked_at"`
	ValidUntil                string `json:"valid_until"`
	AuthorityID               string `json:"authority_id"`
	ReceiptToken              string `json:"receipt_token"`
	AttestationEvidenceBase64 string `json:"attestation_evidence_base64"`
}

// Neither the issuer's identity nor runtime freshness follows from this parser.
func qualificationParseCurrentnessAcknowledgment(data []byte) (qualificationDocument, error) {
	return qualificationParseCurrentnessAcknowledgmentBounded(data, qualificationDocumentLimit, qualificationTotalLimit, nil, nil)
}

func qualificationParseCurrentnessAcknowledgmentBounded(data []byte, documentLimit int, total uint64, role func(string) uint64, materializing func()) (qualificationDocument, error) {
	if err := qualificationProtocolBlobPreflight(data, documentLimit, total, role); err != nil {
		return qualificationDocument{}, err
	}
	if materializing != nil {
		materializing()
	}
	ack, err := qualificationDecode[qualificationCurrentnessAcknowledgment](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	checked, checkedErr := time.Parse(time.RFC3339Nano, ack.CheckedAt)
	until, untilErr := time.Parse(time.RFC3339Nano, ack.ValidUntil)
	if ack.Kind != "waza.qualification-currentness-acknowledgment" || ack.Version != qualificationVersion ||
		!qualificationDigest(ack.RequestSHA256) || !qualificationDigest(ack.CurrentnessProfileSHA256) ||
		!qualificationIdentifier(ack.AuthorityID) || !qualificationIdentifier(ack.ReceiptToken) ||
		(ack.Stage != "before_job" && ack.Stage != "after_cleanup" && ack.Stage != "before_decision") ||
		checkedErr != nil || untilErr != nil || checked.IsZero() || until.IsZero() ||
		checked.UTC().Format(time.RFC3339Nano) != ack.CheckedAt || until.UTC().Format(time.RFC3339Nano) != ack.ValidUntil ||
		!until.After(checked) {
		return qualificationDocument{}, errors.New("qualification: currentness acknowledgment shape")
	}
	if _, err := qualificationBase64Size(ack.AttestationEvidenceBase64, MaxLabelBytes); err != nil {
		return qualificationDocument{}, err
	}
	return qualificationSeal(ack)
}
