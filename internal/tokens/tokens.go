package tokens

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/spboyer/waza/internal/tokens/bpe"
)

type Tokenizer string

const (
	TokenizerBPE      Tokenizer = "bpe"
	TokenizerDefault  Tokenizer = "default"
	TokenizerEstimate Tokenizer = "estimate"

	estimatedCharsPerToken = 4
)

// ValidTokenizers lists supported values for the --tokenizer flag. TokenizerDefault isn't included
// because it's an alias for the default tokenizer and not a distinct option users can specify.
var ValidTokenizers = []string{string(TokenizerBPE), string(TokenizerEstimate)}

func ValidateTokenizer(tokenizer string) error {
	if !slices.Contains(ValidTokenizers, tokenizer) {
		return fmt.Errorf("invalid tokenizer %q: must be one of %s", tokenizer, strings.Join(ValidTokenizers, ", "))
	}
	return nil
}

// Counter counts tokens in text.
type Counter interface {
	Count(text string) int
}

func NewCounter(tokenizer Tokenizer) (Counter, error) {
	switch tokenizer {
	case TokenizerEstimate:
		return &estimatingCounter{}, nil
	case TokenizerBPE, TokenizerDefault:
		return newBPECounter()
	default:
		return nil, fmt.Errorf("unsupported tokenizer %q", tokenizer)
	}
}

// estimatingCounter approximates token count as ~4 characters per token.
type estimatingCounter struct{}

func (*estimatingCounter) Count(text string) int {
	return Estimate(text)
}

func Estimate(text string) int {
	return int(math.Ceil(float64(len(text)) / float64(estimatedCharsPerToken)))
}

// bpeCounter counts tokens using byte-pair encoding.
type bpeCounter struct {
	tokenizer *bpe.Tokenizer
}

// newBPECounter returns a counter that uses a byte-pair encoding similar to that used by the most recent models.
func newBPECounter() (*bpeCounter, error) {
	// the underlying bpe package takes a model name argument to simplify supporting multiple encodings in the
	// future, but for now this constructor hardcodes that argument and doesn't take any arguments itself
	// because the bpe package supports only one encoding
	tok, err := bpe.NewTokenizerForModel("gpt-5", nil)
	if err != nil {
		return nil, fmt.Errorf("initializing tokenizer: %w", err)
	}
	return &bpeCounter{tok}, nil
}

func (b *bpeCounter) Count(text string) int {
	return len(b.tokenizer.Encode(text, nil))
}
