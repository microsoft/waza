package bpe

import (
	"embed"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
)

var (
	modelPrefixToEncoding = []struct {
		Prefix   string
		Encoding string
	}{
		{Prefix: "gpt-5", Encoding: "o200k_base"},
		{Prefix: "gpt-4.", Encoding: "o200k_base"},
	}

	// ModelToEncoding maps model names to the tokenizer encoding used by the model.
	ModelToEncoding = map[string]string{
		"gpt-4o": "o200k_base",
	}

	regexPatternO200k = strings.Join([]string{
		`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?:'s|'S|'t|'T|'re|'RE|'Re|'eR|'ve|'VE|'vE|'Ve|'m|'M|'ll|'lL|'Ll|'LL|'d|'D)?`,
		`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?:'s|'S|'t|'T|'re|'RE|'Re|'eR|'ve|'VE|'vE|'Ve|'m|'M|'ll|'lL|'Ll|'LL|'d|'D)?`,
		`\p{N}{1,3}`,
		` ?[^\s\p{L}\p{N}]+[\r\n/]*`,
		`\s*[\r\n]+`,
		`\s+`,
	}, "|")

	//go:embed models
	modelFS embed.FS
)

const (
	endOfText   = "<|endoftext|>"
	endOfPrompt = "<|endofprompt|>"
)

// Regex patterns mirrored from tokenizer_ts with stdlib-compatible whitespace handling.
const (
	regexPatternLegacy = `'s|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+`
	regexPatternModern = `(?:'s|'S|'t|'T|'re|'RE|'Re|'eR|'ve|'VE|'vE|'Ve|'m|'M|'ll|'lL|'Ll|'LL|'d|'D)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+`
)

func encodingForModelName(modelName string) string {
	if encoding, ok := ModelToEncoding[modelName]; ok {
		return encoding
	}
	for _, entry := range modelPrefixToEncoding {
		if strings.HasPrefix(modelName, entry.Prefix) {
			return entry.Encoding
		}
	}
	return ""
}

func mergeSpecialTokens(base map[string]int, extra map[string]int) map[string]int {
	out := maps.Clone(base)
	maps.Copy(out, extra)
	return out
}

func bpeFileForEncoding(encoding string) (string, string, error) {
	switch encoding {
	case "o200k_base":
		return regexPatternO200k, "o200k_base.tiktoken", nil
	default:
		return "", "", errors.New(encoding + " encoding isn't supported")
	}
}

func SpecialTokensForEncoding(encoding string) map[string]int {
	specialTokens := map[string]int{endOfText: 50256}
	switch encoding {
	case "o200k_base":
		specialTokens = map[string]int{
			endOfText:   199999,
			endOfPrompt: 200018,
		}
	}
	return specialTokens
}

func SpecialTokensForModel(modelName string) map[string]int {
	return SpecialTokensForEncoding(encodingForModelName(modelName))
}

func RegexForEncoding(encoding string) string {
	switch encoding {
	case "o200k_base":
		return regexPatternO200k
	default:
		return regexPatternLegacy
	}
}

func RegexForModel(modelName string) string {
	return RegexForEncoding(encodingForModelName(modelName))
}

func EncodingForModel(modelName string) (string, error) {
	encoding := encodingForModelName(modelName)
	if encoding == "" {
		return "", fmt.Errorf("doesn't support this model [%s]", modelName)
	}
	return encoding, nil
}

func NewTokenizerForModel(modelName string, extraSpecialTokens map[string]int) (*Tokenizer, error) {
	encoding, err := EncodingForModel(modelName)
	if err != nil {
		return nil, err
	}
	return NewTokenizerForEncoding(encoding, extraSpecialTokens)
}

func NewTokenizerForEncoding(encoding string, extraSpecialTokens map[string]int) (*Tokenizer, error) {
	regexPattern, fileName, err := bpeFileForEncoding(encoding)
	if err != nil {
		return nil, err
	}

	specialTokens := SpecialTokensForEncoding(encoding)
	if extraSpecialTokens != nil {
		specialTokens = mergeSpecialTokens(specialTokens, extraSpecialTokens)
	}

	f, err := modelFS.Open(filepath.Join("models", fileName))
	if err != nil {
		return nil, fmt.Errorf("failed to open embedded model file %s: %w", fileName, err)
	}
	defer f.Close() //nolint:errcheck

	return NewTokenizerFromReader(f, specialTokens, regexPattern, defaultCacheSize)
}
