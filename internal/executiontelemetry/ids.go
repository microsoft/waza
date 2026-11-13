package executiontelemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TraceID derives the OTLP trace ID for one logical work item. Every attempt,
// retry and branch of the same (run, work) pair shares one trace, and any
// producer that knows the public identifiers derives the same value.
func TraceID(run, work string) string {
	return derive("waza-trace-v1", 16, run, work)
}

// SpanID derives a span ID from its stable signal kind and correlation key.
func SpanID(kind string, key ...string) string {
	return derive("waza-span-v1", 8, append([]string{kind}, key...)...)
}

// WorkSpanID is the root span of one logical work item.
func WorkSpanID(run, work string) string { return SpanID(SpanWork, run, work) }

func derive(domain string, size int, parts ...string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + strings.Join(parts, "\x00")))
	id := sum[:size]
	// An all-zero ID is invalid in OTLP; flip the final bit in that edge case.
	allZero := true
	for _, b := range id {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		id[size-1] = 1
	}
	return hex.EncodeToString(id)
}
