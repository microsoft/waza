package tokens

import (
	"math"
)

const charsPerToken = 4

// Counter counts tokens in text.
type Counter interface {
	Count(text string) int
}

// EstimatingCounter approximates token count as ~4 characters per token.
type EstimatingCounter struct{}

func NewEstimatingCounter() *EstimatingCounter {
	return &EstimatingCounter{}
}

func (*EstimatingCounter) Count(text string) int {
	return Estimate(text)
}

func Estimate(text string) int {
	return int(math.Ceil(float64(len(text)) / float64(charsPerToken)))
}
