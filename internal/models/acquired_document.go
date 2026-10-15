package models

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// AcquiredOfflineDocument owns bounded, source-bound bytes. Its label is for
// diagnostics only and is never reopened by the selected native decoders.
type AcquiredOfflineDocument struct {
	label string
	data  []byte
}

func AcquireOfflineDocument(label, expectedSHA string, data []byte) (*AcquiredOfflineDocument, error) {
	if label == "" || expectedSHA == "" || len(data) == 0 || len(data) > 16*1024*1024 {
		return nil, errors.New("acquired document requires a label, content binding and 1 to 16777216 bytes")
	}
	owned := bytes.Clone(data)
	sum := sha256.Sum256(owned)
	if hex.EncodeToString(sum[:]) != expectedSHA {
		return nil, errors.New("acquired document content binding mismatch")
	}
	return &AcquiredOfflineDocument{label: label, data: owned}, nil
}

func LoadAcquiredEvalSpec(document *AcquiredOfflineDocument) (*EvalSpec, error) {
	if document == nil || len(document.data) == 0 {
		return nil, errors.New("acquired eval document is required")
	}
	return parseEvalSpec(document.data, document.label, true, nil)
}

func LoadAcquiredTestCase(document *AcquiredOfflineDocument) (*TestCase, error) {
	if document == nil || len(document.data) == 0 {
		return nil, errors.New("acquired task document is required")
	}
	task, err := parseTestCase(document.data, document.label, true, func(string) ([]byte, error) {
		return nil, errors.New("acquired task does not support prompt_file")
	}, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(task.TestID) == "" {
		return nil, errors.New("acquired task requires an explicit nonblank id")
	}
	return task, nil
}
