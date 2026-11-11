package artifactmanifest_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestProductionRunnerPublicationMatchesFrozenBytes(t *testing.T) {
	request, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		t.Fatal(err)
	}
	envelope, binding := runnerunit.NewService(nil).RunTerminal(context.Background(), request)
	if binding == nil || envelope.Outcome != runnerterminal.Success || envelope.EngineCalls != 1 {
		t.Fatalf("actual mock runner did not execute the selected unit: %+v", envelope)
	}
	var emitted bytes.Buffer
	code, err := runnerterminal.NewEmitter(&emitted).Emit(envelope, binding)
	if err != nil || code != 0 {
		t.Fatalf("actual terminal emission failed: %d %v", code, err)
	}
	// Frozen from `waza runner run < examples/runner/unit.json`, including its LF.
	terminal, err := os.ReadFile("testdata/publication-terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(terminal, emitted.Bytes()) {
		t.Fatal("actual full terminal differs from the frozen native CLI output")
	}
	manifest := artifactmanifest.Manifest{Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: artifactmanifest.Binding{Identity: *binding.Identity, Selection: binding.Selection,
			ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs},
		TerminalArtifact: "terminal.json",
		Artifacts: []artifactmanifest.Artifact{{Name: "terminal.json", SHA256: artifactmanifest.Hash(terminal),
			SizeBytes: int64(len(terminal)), MediaType: "application/json"}},
	}
	wire, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	frozenManifest, err := os.ReadFile("testdata/publication-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wire, bytes.TrimSuffix(frozenManifest, []byte("\n"))) {
		t.Fatal("actual full manifest/binding differs from the frozen publication declaration")
	}
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	}()
	store := artifactmanifest.NewStore(backend)
	digest, err := store.Create(context.Background(), wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), manifest.Artifacts[0], terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ReadCompleted(context.Background(), digest)
	if err != nil || !bytes.Equal(completed.ManifestBytes, wire) || !bytes.Equal(completed.ArtifactBytes["terminal.json"], terminal) {
		t.Fatalf("production publication did not retain exact verified bytes: %v", err)
	}
}
