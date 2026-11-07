package executionattempt

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// NewExecutionID allocates an opaque identifier independent of native ordinal
// and logical identity. The execution owner must still reserve it conditionally.
func NewExecutionID() (string, error) {
	return newID("exec-", rand.Reader)
}

func NewDecisionID() (string, error) {
	return newID("decision-", rand.Reader)
}

func newID(prefix string, entropy io.Reader) (string, error) {
	var value [32]byte
	if _, err := io.ReadFull(entropy, value[:]); err != nil {
		return "", fmt.Errorf("generating %sidentifier: %w", prefix, err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
