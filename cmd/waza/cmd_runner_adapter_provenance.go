package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"

	"github.com/microsoft/waza/internal/runneradapter"
)

const (
	provenanceVerifierTimeout = 30 * time.Second
	provenanceVerifierMaxIO   = 16 << 10
)

type commandProvenanceVerifier struct {
	path string
	args []string
}

func (v commandProvenanceVerifier) VerifyProvenance(ctx context.Context, manifestDigest string, public json.RawMessage) runneradapter.Provenance {
	ctx, cancel := context.WithTimeout(ctx, provenanceVerifierTimeout)
	defer cancel()

	request, err := json.Marshal(struct {
		ManifestDigest string          `json:"manifest_digest"`
		PublicResult   json.RawMessage `json:"public_result"`
	}{ManifestDigest: manifestDigest, PublicResult: public})
	if err != nil || len(request) > provenanceVerifierMaxIO {
		return runneradapter.Provenance{State: "unavailable", Detail: "provenance verifier request is invalid or too large"}
	}

	command := exec.CommandContext(ctx, v.path, v.args...)
	command.Stdin = bytes.NewReader(request)
	output := &boundedVerifierOutput{}
	command.Stdout = output
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return runneradapter.Provenance{State: "unavailable", Detail: "provenance verifier timed out or was canceled"}
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return runneradapter.Provenance{State: "failed", Detail: "configured provenance verifier rejected or failed to verify the manifest"}
		}
		return runneradapter.Provenance{State: "unavailable", Detail: "configured provenance verifier could not be started"}
	}

	var answer struct {
		State string `json:"state"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil || decoder.Decode(new(any)) != io.EOF {
		return runneradapter.Provenance{State: "unavailable", Detail: "configured provenance verifier returned an invalid response"}
	}
	switch answer.State {
	case "verified", "failed", "unavailable":
		return runneradapter.Provenance{State: answer.State}
	default:
		return runneradapter.Provenance{State: "unavailable", Detail: "configured provenance verifier returned an unknown state"}
	}
}

type boundedVerifierOutput struct {
	bytes.Buffer
}

func (b *boundedVerifierOutput) Write(data []byte) (int, error) {
	if len(data) > provenanceVerifierMaxIO-b.Len() {
		return 0, errors.New("provenance verifier response exceeds the size limit")
	}
	return b.Buffer.Write(data)
}
