package bpe

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const defaultCacheSize = 8192

type whitespaceSplitMode int

const (
	whitespaceSplitNone whitespaceSplitMode = iota
	whitespaceSplitAll
	whitespaceSplitNoNewline
)

type EncodeResult struct {
	TokenIDs []int
	Text     string
}

type Tokenizer struct {
	pieceRegex           *regexp.Regexp
	encoder              *BinaryMap[int]
	decoder              map[int][]byte
	specialTokensRegex   *regexp.Regexp
	specialTokensEncoder map[string]int
	specialTokensDecoder map[int]string
	whitespaceSplitMode  whitespaceSplitMode
	cache                *LRUCache
}

func NewTokenizerFromReader(
	r io.Reader,
	specialTokensEncoder map[string]int,
	regexPattern string,
	cacheSize int,
) (*Tokenizer, error) {
	bpeDict, err := loadTikTokenBPEFromReader(r)
	if err != nil {
		return nil, err
	}
	return NewTokenizerFromRanks(bpeDict, specialTokensEncoder, regexPattern, cacheSize)
}

// NewTokenizerFromRanks expects keys to be raw byte sequences represented as string.
func NewTokenizerFromRanks(
	bpeDict map[string]int,
	specialTokensEncoder map[string]int,
	regexPattern string,
	cacheSize int,
) (*Tokenizer, error) {
	if cacheSize <= 0 {
		cacheSize = defaultCacheSize
	}

	pieceRegex, err := regexp.Compile("^(?:" + regexPattern + ")")
	if err != nil {
		return nil, fmt.Errorf("failed to compile piece regex pattern: %w", err)
	}

	specialRegex, err := compileSpecialTokensRegex(specialTokensEncoder)
	if err != nil {
		return nil, fmt.Errorf("failed to compile special token regex: %w", err)
	}

	t := &Tokenizer{
		pieceRegex:           pieceRegex,
		encoder:              NewBinaryMap[int](),
		decoder:              map[int][]byte{},
		specialTokensRegex:   specialRegex,
		specialTokensEncoder: maps.Clone(specialTokensEncoder),
		specialTokensDecoder: map[int]string{},
		whitespaceSplitMode:  detectWhitespaceSplitMode(regexPattern),
		cache:                NewLRUCache(cacheSize),
	}

	for key, value := range bpeDict {
		t.encoder.Set([]byte(key), value)
		t.decoder[value] = []byte(key)
	}
	if len(bpeDict) != len(t.decoder) {
		return nil, fmt.Errorf("encoder and decoder sizes do not match")
	}

	for key, value := range t.specialTokensEncoder {
		t.specialTokensDecoder[value] = key
	}

	return t, nil
}

func loadTikTokenBPEFromReader(r io.Reader) (map[string]int, error) {
	bpeDict := map[string]int{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		tokens := strings.Fields(line)
		if len(tokens) != 2 {
			return nil, fmt.Errorf("invalid format in the BPE encoder file stream")
		}

		tokenBytes, err := base64.StdEncoding.DecodeString(tokens[0])
		if err != nil {
			return nil, fmt.Errorf("invalid base64 token %q: %w", tokens[0], err)
		}
		rank, err := strconv.Atoi(tokens[1])
		if err != nil {
			return nil, fmt.Errorf("can't parse %s to integer", tokens[1])
		}
		bpeDict[string(tokenBytes)] = rank
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to load from BPE encoder file stream: %w", err)
	}
	return bpeDict, nil
}

func compileSpecialTokensRegex(specialTokens map[string]int) (*regexp.Regexp, error) {
	if len(specialTokens) == 0 {
		return nil, nil
	}

	keys := make([]string, 0, len(specialTokens))
	for key := range specialTokens {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) == len(keys[j]) {
			return keys[i] < keys[j]
		}
		return len(keys[i]) > len(keys[j])
	})

	escaped := make([]string, 0, len(keys))
	for _, key := range keys {
		escaped = append(escaped, regexp.QuoteMeta(key))
	}
	return regexp.Compile(strings.Join(escaped, "|"))
}

func makeStringSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func detectWhitespaceSplitMode(regexPattern string) whitespaceSplitMode {
	switch regexPattern {
	case regexPatternLegacy:
		return whitespaceSplitAll
	case regexPatternModern, regexPatternO200k:
		return whitespaceSplitNoNewline
	default:
		return whitespaceSplitNone
	}
}

func whitespaceOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func splitLastRune(s string) (prefix string, suffix string) {
	if s == "" {
		return "", ""
	}
	lastStart := len(s)
	for i := range s {
		lastStart = i
	}
	return s[:lastStart], s[lastStart:]
}

func (t *Tokenizer) pieces(substring string) []string {
	out := make([]string, 0)
	for len(substring) > 0 {
		loc := t.pieceRegex.FindStringIndex(substring)
		if loc == nil || loc[0] != 0 || loc[1] <= 0 {
			_, size := utf8.DecodeRuneInString(substring)
			if size <= 0 {
				break
			}
			out = append(out, substring[:size])
			substring = substring[size:]
			continue
		}

		piece := substring[:loc[1]]
		if trimmed, ok := t.trimWhitespaceForLookahead(piece, substring); ok {
			piece = trimmed
		}
		out = append(out, piece)
		substring = substring[len(piece):]
	}
	return out
}

func (t *Tokenizer) trimWhitespaceForLookahead(piece, source string) (string, bool) {
	if t.whitespaceSplitMode == whitespaceSplitNone || !whitespaceOnly(piece) {
		return piece, false
	}
	if t.whitespaceSplitMode == whitespaceSplitNoNewline &&
		(strings.ContainsRune(piece, '\n') || strings.ContainsRune(piece, '\r')) {
		return piece, false
	}
	if len(piece) >= len(source) {
		return piece, false
	}

	nextRune, _ := utf8.DecodeRuneInString(source[len(piece):])
	if unicode.IsSpace(nextRune) {
		return piece, false
	}
	if utf8.RuneCountInString(piece) <= 1 {
		return piece, false
	}

	prefix, _ := splitLastRune(piece)
	if prefix == "" {
		return piece, false
	}
	return prefix, true
}

func (t *Tokenizer) findNextSpecialToken(
	text string,
	start int,
	allowedSpecial map[string]struct{},
) (token string, matchStart int, end int, found bool) {
	if start >= len(text) {
		return "", -1, len(text), false
	}
	if len(allowedSpecial) == 0 || t.specialTokensRegex == nil {
		return "", -1, len(text), false
	}

	startFind := start
	for startFind < len(text) {
		sub := text[startFind:]
		loc := t.specialTokensRegex.FindStringIndex(sub)
		if loc == nil {
			break
		}
		foundToken := sub[loc[0]:loc[1]]
		if _, ok := allowedSpecial[foundToken]; ok {
			absStart := startFind + loc[0]
			return foundToken, absStart, absStart, true
		}
		startFind += loc[0] + 1
	}
	return "", -1, len(text), false
}

func (t *Tokenizer) Encode(text string, allowedSpecial []string) []int {
	tokenIDs := []int{}
	allowedSpecialSet := makeStringSet(allowedSpecial)

	start := 0
	for {
		nextSpecial, specialStart, end, found := t.findNextSpecialToken(
			text,
			start,
			allowedSpecialSet,
		)
		if end > start {
			t.encodeByIndex(text, &tokenIDs, start, end)
		}

		if found {
			if token, ok := t.specialTokensEncoder[nextSpecial]; ok {
				tokenIDs = append(tokenIDs, token)
			}
			start = specialStart + len(nextSpecial)
			if start >= len(text) {
				break
			}
		} else {
			break
		}
	}

	return tokenIDs
}

func (t *Tokenizer) encodeByIndex(text string, tokenIDs *[]int, start, end int) {
	substring := text[start:end]
	matches := t.pieces(substring)
	for _, piece := range matches {
		if cached, ok := t.cache.Get(piece); ok {
			*tokenIDs = append(*tokenIDs, cached...)
			continue
		}

		bytes := []byte(piece)
		token, ok := t.encoder.GetRange(bytes, 0, len(bytes))
		if ok {
			*tokenIDs = append(*tokenIDs, token)
			t.cache.Set(piece, []int{token})
			continue
		}

		encodedTokens := BytePairEncode(bytes, t.encoder, len(bytes))
		*tokenIDs = append(*tokenIDs, encodedTokens...)
		t.cache.Set(piece, encodedTokens)
	}
}

func (t *Tokenizer) encodeTrimSuffixByIndex(
	text string,
	tokenIDs *[]int,
	start, end int,
	maxTokenCount int,
	tokenCount int,
	encodeLength int,
) (int, int) {
	substring := text[start:end]
	matches := t.pieces(substring)
	for _, piece := range matches {
		if cachedTokens, ok := t.cache.Get(piece); ok {
			if tokenCount+len(cachedTokens) <= maxTokenCount {
				tokenCount += len(cachedTokens)
				encodeLength += len(piece)
				*tokenIDs = append(*tokenIDs, cachedTokens...)
			} else {
				remainingTokens := maxTokenCount - tokenCount
				if remainingTokens < 0 {
					remainingTokens = 0
				}
				tokenCount += remainingTokens
				encodeLength += len(piece)
				*tokenIDs = append(*tokenIDs, cachedTokens[:remainingTokens]...)
				break
			}
		} else {
			bytes := []byte(piece)
			token, ok := t.encoder.GetRange(bytes, 0, len(bytes))
			if ok {
				t.cache.Set(piece, []int{token})
				if tokenCount+1 <= maxTokenCount {
					tokenCount++
					encodeLength += len(piece)
					*tokenIDs = append(*tokenIDs, token)
				} else {
					break
				}
			} else {
				encodedTokens := BytePairEncode(bytes, t.encoder, len(bytes))
				t.cache.Set(piece, encodedTokens)
				if tokenCount+len(encodedTokens) <= maxTokenCount {
					tokenCount += len(encodedTokens)
					encodeLength += len(piece)
					*tokenIDs = append(*tokenIDs, encodedTokens...)
				} else {
					remainingTokens := maxTokenCount - tokenCount
					if remainingTokens < 0 {
						remainingTokens = 0
					}
					tokenCount += remainingTokens
					encodeLength += len(piece)
					*tokenIDs = append(*tokenIDs, encodedTokens[:remainingTokens]...)
					break
				}
			}
		}

		if tokenCount >= maxTokenCount {
			break
		}
	}

	return tokenCount, encodeLength
}

func (t *Tokenizer) EncodeTrimSuffix(
	text string,
	maxTokenCount int,
	allowedSpecial []string,
) EncodeResult {
	if maxTokenCount <= 0 {
		return EncodeResult{
			TokenIDs: []int{},
			Text:     "",
		}
	}

	tokenIDs := []int{}
	start := 0
	tokenCount := 0
	encodeLength := 0
	allowedSpecialSet := makeStringSet(allowedSpecial)

	for {
		nextSpecial, specialStart, end, found := t.findNextSpecialToken(
			text,
			start,
			allowedSpecialSet,
		)
		if end > start {
			tokenCount, encodeLength = t.encodeTrimSuffixByIndex(
				text,
				&tokenIDs,
				start,
				end,
				maxTokenCount,
				tokenCount,
				encodeLength,
			)
			if tokenCount >= maxTokenCount {
				break
			}
		}

		if found {
			tokenCount++
			if tokenCount <= maxTokenCount {
				if token, ok := t.specialTokensEncoder[nextSpecial]; ok {
					tokenIDs = append(tokenIDs, token)
				}
				start = specialStart + len(nextSpecial)
				encodeLength += len(nextSpecial)
				if start >= len(text) {
					break
				}
			}
			if tokenCount >= maxTokenCount {
				break
			}
		} else {
			break
		}
	}

	encodedText := text
	if encodeLength < len(text) {
		encodedText = text[:encodeLength]
	}
	return EncodeResult{
		TokenIDs: tokenIDs,
		Text:     encodedText,
	}
}

type tokenProgress struct {
	tokenCount   int
	encodeLength int
}

func (t *Tokenizer) EncodeTrimPrefix(
	text string,
	maxTokenCount int,
	allowedSpecial []string,
) EncodeResult {
	tokenIDs := []int{}
	start := 0
	tokenCount := 0
	encodeLength := 0
	tokenCountMap := []tokenProgress{{tokenCount: tokenCount, encodeLength: encodeLength}}
	allowedSpecialSet := makeStringSet(allowedSpecial)

	for {
		nextSpecial, specialStart, end, found := t.findNextSpecialToken(
			text,
			start,
			allowedSpecialSet,
		)
		if end > start {
			substring := text[start:end]
			matches := t.pieces(substring)
			for _, piece := range matches {
				if cachedTokens, ok := t.cache.Get(piece); ok {
					tokenCount += len(cachedTokens)
					encodeLength += len(piece)
					tokenIDs = append(tokenIDs, cachedTokens...)
					tokenCountMap = append(tokenCountMap, tokenProgress{
						tokenCount:   tokenCount,
						encodeLength: encodeLength,
					})
				} else {
					bytes := []byte(piece)
					token, ok := t.encoder.GetRange(bytes, 0, len(bytes))
					if ok {
						t.cache.Set(piece, []int{token})
						tokenCount++
						encodeLength += len(piece)
						tokenIDs = append(tokenIDs, token)
						tokenCountMap = append(tokenCountMap, tokenProgress{
							tokenCount:   tokenCount,
							encodeLength: encodeLength,
						})
					} else {
						encodedTokens := BytePairEncode(bytes, t.encoder, len(bytes))
						t.cache.Set(piece, encodedTokens)
						tokenCount += len(encodedTokens)
						encodeLength += len(piece)
						tokenIDs = append(tokenIDs, encodedTokens...)
						tokenCountMap = append(tokenCountMap, tokenProgress{
							tokenCount:   tokenCount,
							encodeLength: encodeLength,
						})
					}
				}
			}
		}

		if found {
			if token, ok := t.specialTokensEncoder[nextSpecial]; ok {
				tokenIDs = append(tokenIDs, token)
			}
			start = specialStart + len(nextSpecial)
			tokenCount++
			encodeLength += len(nextSpecial)
			tokenCountMap = append(tokenCountMap, tokenProgress{
				tokenCount:   tokenCount,
				encodeLength: encodeLength,
			})
			if start >= len(text) {
				break
			}
		} else {
			break
		}
	}

	if tokenCount <= maxTokenCount {
		return EncodeResult{
			TokenIDs: tokenIDs,
			Text:     text,
		}
	}

	prefixTokenCount := tokenCount - maxTokenCount
	actualPrefixTokenCount := 0
	actualPrefixStrLength := 0
	for _, entry := range tokenCountMap {
		if entry.tokenCount >= prefixTokenCount {
			actualPrefixTokenCount = entry.tokenCount
			actualPrefixStrLength = entry.encodeLength
			break
		}
	}

	// Naive approach if chunks are incorrect.
	if actualPrefixTokenCount > maxTokenCount {
		encodedTokens := t.Encode(text, allowedSpecial)
		if maxTokenCount <= 0 {
			return EncodeResult{
				TokenIDs: []int{},
				Text:     "",
			}
		}
		if len(encodedTokens) > maxTokenCount {
			slicedTokens := encodedTokens[len(encodedTokens)-maxTokenCount:]
			return EncodeResult{
				TokenIDs: slicedTokens,
				Text:     t.Decode(slicedTokens),
			}
		}
		return EncodeResult{
			TokenIDs: encodedTokens,
			Text:     t.Decode(encodedTokens),
		}
	}

	if actualPrefixTokenCount > len(tokenIDs) {
		actualPrefixTokenCount = len(tokenIDs)
	}
	if actualPrefixStrLength > len(text) {
		actualPrefixStrLength = len(text)
	}

	return EncodeResult{
		TokenIDs: tokenIDs[actualPrefixTokenCount:],
		Text:     text[actualPrefixStrLength:],
	}
}

func (t *Tokenizer) Decode(tokens []int) string {
	decoded := make([]byte, 0)
	for _, token := range tokens {
		if value, ok := t.decoder[token]; ok {
			decoded = append(decoded, value...)
			continue
		}
		if specialValue, ok := t.specialTokensDecoder[token]; ok {
			decoded = append(decoded, []byte(specialValue)...)
		}
	}
	return string(decoded)
}
