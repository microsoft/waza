package nativesnapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestCollectionCaptureRawWireAdmission(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{"duplicate outer member", `{"kind":"first","kind":"second"}`, "duplicate"},
		{"duplicate nested snapshot member", `{"snapshot":{"baseline":{},"baseline":{}}}`, "duplicate"},
		{"duplicate blob member", `{"blobs":{"same":"","same":""}}`, "duplicate"},
		{"case alias", `{"Kind":"waza.release-native-capture"}`, "exact spelling"},
		{"unknown member", `{"unexpected":true}`, "unknown field"},
		{"malformed", `{"kind":`, "invalid JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			capture, err := ReadCollectionCapture(t.Context(), []byte(test.raw))
			require.ErrorContains(t, err, test.want)
			require.Nil(t, capture)
		})
	}
}

func TestCollectionCaptureExecutableBounds(t *testing.T) {
	base, locations := fixture(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	input, err := os.Open(executable)
	require.NoError(t, err)
	output, err := os.OpenFile(filepath.Join(base, "evaluator"), os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, copyErr := io.Copy(output, input)
	require.NoError(t, input.Close())
	require.NoError(t, copyErr)
	info, err := output.Stat()
	require.NoError(t, err)
	size := max(info.Size(), int64(maxExecutableBytes+1))
	require.LessOrEqual(t, size, int64(CollectionExecutableBytes))
	require.NoError(t, output.Truncate(size))
	require.NoError(t, output.Close())
	_, err = Prepare(t.Context(), locations)
	require.ErrorContains(t, err, "exceeds source limit", "private executable bound stays 128 MiB")
	captured, err := CaptureCollection(t.Context(), locations)
	require.NoError(t, err, "actual evaluator image with retained trailing padding is not a substitute executable")
	require.NoError(t, captured.Recheck(t.Context()))
	prepared, err := captured.Primitive(t.Context(), releasepolicy.Baseline, "task")
	require.NoError(t, err, "public per-attempt source descriptor must admit the same evaluator")
	require.NotNil(t, prepared)
	raw, err := captured.Marshal(t.Context())
	require.NoError(t, err)
	retained, err := ReadCollectionCapture(t.Context(), raw)
	require.NoError(t, err)
	before, err := captured.Projections(t.Context())
	require.NoError(t, err)
	after, err := retained.Projections(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	t.Logf("retained actual evaluator: %d bytes; capture wire: %d bytes", size, len(raw))

	require.NoError(t, os.Truncate(filepath.Join(base, "evaluator"), CollectionExecutableBytes+1))
	_, err = CaptureCollection(t.Context(), locations)
	require.ErrorContains(t, err, "exceeds source limit", "public executable bound stays 256 MiB")
	require.NoError(t, os.Truncate(filepath.Join(base, "evaluator"), CollectionExecutableBytes))
	_, err = CaptureCollection(t.Context(), locations)
	require.ErrorContains(t, err, "candidate native capture", "the first exact-256 MiB evaluator fits its per-file allowance")
	require.ErrorContains(t, err, "captured source byte limit", "two executable inventories plus declarations exceed the 512 MiB aggregate")
}

func TestCollectionLimitSelectionAndWireBoundary(t *testing.T) {
	executable, total := sourceByteLimits(context.Background())
	require.Equal(t, 128<<20, executable)
	require.Equal(t, 256<<20, total)
	executable, total = sourceByteLimits(collectionContext(t.Context()))
	require.Equal(t, 256<<20, executable)
	require.Equal(t, 512<<20, total)
	require.NoError(t, collectionWireSize(1<<30))
	require.ErrorContains(t, collectionWireSize(1<<30+1), "retained byte limit")
	require.Nil(t, collectionContext(nil)) //nolint:staticcheck // Explicit nil-context boundary.
	_, err := CaptureCollection(nil, nil)  //nolint:staticcheck // Explicit nil-context boundary.
	require.Error(t, err)
}

// BenchmarkReadCollectionCaptureActualExecutable measures the production reader
// against a real test executable at the admitted descriptor boundary.
func BenchmarkReadCollectionCaptureActualExecutable(b *testing.B) {
	base, locations := fixture(b)
	executable := os.Getenv("WAZA_CAPTURE_BENCHMARK_EXECUTABLE")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		require.NoError(b, err)
	}
	input, err := os.Open(executable)
	require.NoError(b, err)
	output, err := os.OpenFile(filepath.Join(base, "evaluator"), os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(b, err)
	_, copyErr := io.Copy(output, input)
	require.NoError(b, input.Close())
	require.NoError(b, copyErr)
	info, err := output.Stat()
	require.NoError(b, err)
	require.NoError(b, output.Truncate(max(info.Size(), int64(maxExecutableBytes+1))))
	require.NoError(b, output.Close())
	captured, err := CaptureCollection(b.Context(), locations)
	require.NoError(b, err)
	raw, err := captured.Marshal(b.Context())
	require.NoError(b, err)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		retained, err := ReadCollectionCapture(b.Context(), raw)
		require.NoError(b, err)
		require.NotNil(b, retained)
	}
}

func TestCollectionCaptureBufferOwnershipAndCurrentness(t *testing.T) {
	base, locations := fixture(t)
	write(t, base, "baseline/waza.lock", "")
	captured, err := CaptureCollection(t.Context(), locations)
	require.NoError(t, err)
	emptyFound := false
	for _, entry := range captured.capture.prepared.sources[releasepolicy.Baseline] {
		if entry.Path == "baseline/waza.lock" {
			emptyFound = true
			require.Nil(t, entry.Bytes, "empty sources retain their frozen nil representation")
			require.Equal(t, releasepolicy.SourceDigest(nil), entry.Digest)
		}
	}
	require.True(t, emptyFound)
	request, err := captured.Request(t.Context(), releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.Equal(t, []byte("baseline"), request.Resources[0].Content)
	request.Resources[0].Content[0] = '!'
	detached, err := captured.Request(t.Context(), releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.Equal(t, []byte("baseline"), detached.Resources[0].Content)
	require.NoError(t, captured.Recheck(t.Context()))

	write(t, base, "baseline/context/a.txt", "modified")
	require.Error(t, captured.Recheck(t.Context()), "same-length backing-file changes must invalidate currentness")
	detached, err = captured.Request(t.Context(), releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.Equal(t, []byte("baseline"), detached.Resources[0].Content)
	recaptured, err := CaptureCollection(t.Context(), locations)
	require.NoError(t, err)
	current, err := recaptured.Request(t.Context(), releasepolicy.Baseline, "task")
	require.NoError(t, err)
	require.Equal(t, []byte("modified"), current.Resources[0].Content)
}

func TestSourceInventoryOwnershipHandoff(t *testing.T) {
	files, base := reader(t, t.Context())
	write(t, base, "input", "original")
	write(t, base, "empty", "")
	first, err := files.ReadFile(filepath.Join(base, "input"))
	require.NoError(t, err)
	cached, err := files.ReadFile(filepath.Join(base, "input"))
	require.NoError(t, err)
	empty, err := files.ReadFile(filepath.Join(base, "empty"))
	require.NoError(t, err)
	require.Nil(t, empty)
	owned := &files.data["input"][0]
	retainedMap := files.data
	inventory := files.takeInventory()
	require.Len(t, inventory, 2)
	require.Empty(t, retainedMap, "even a retained map must no longer reference transferred buffers")
	require.True(t, files.frozen)
	require.Equal(t, "empty", inventory[0].Path)
	require.Nil(t, inventory[0].Bytes)
	require.Equal(t, releasepolicy.SourceDigest(nil), inventory[0].Digest)
	require.Equal(t, "input", inventory[1].Path)
	require.Same(t, owned, &inventory[1].Bytes[0], "terminal handoff must not clone the owned buffer")
	first[0], cached[0] = '!', '?'
	require.Equal(t, []byte("original"), inventory[1].Bytes, "all prior ReadFile results remain detached")
	require.Equal(t, releasepolicy.SourceDigest([]byte("original")), inventory[1].Digest)
	require.Equal(t, []string{"input"}, inventory[1].Roles)
	files.roles["input"]["later"] = true
	require.Equal(t, []string{"input"}, inventory[1].Roles)
	write(t, base, "input", "modified")
	_, err = files.ReadFile(filepath.Join(base, "input"))
	require.ErrorContains(t, err, "uncaptured input", "a consumed reader must not reopen transferred sources")
	require.Equal(t, []byte("original"), inventory[1].Bytes)
	require.Equal(t, []source{}, files.takeInventory(), "a repeated handoff must not retain or publish aliases")

	for _, data := range [][]byte{nil, {}} {
		files, _ := reader(t, t.Context())
		files.data["empty"] = data
		transferred := files.takeInventory()
		require.Len(t, transferred, 1)
		require.Equal(t, data, transferred[0].Bytes, "handoff preserves nil versus nonnil empty bytes")
	}
}

func TestCollectionCaptureTransferredInventoryMatchesFrozenSource(t *testing.T) {
	for _, emptyLock := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent_lock", true: "empty_lock"}[emptyLock], func(t *testing.T) {
			base, locations := fixture(t)
			if emptyLock {
				write(t, base, "baseline/waza.lock", "")
			}
			original := frozenOriginalCapture(t, locations)
			captured, err := CaptureCollection(t.Context(), locations)
			require.NoError(t, err)
			require.Equal(t, original.Canonical, captured.capture.prepared.canonical)
			require.Equal(t, original.Sources, captured.capture.prepared.sources)
			projections, err := captured.Projections(t.Context())
			require.NoError(t, err)
			require.Equal(t, original.Projection, projections)
			wire, err := captured.Marshal(t.Context())
			require.NoError(t, err)
			for range 2 {
				again, err := CaptureCollection(t.Context(), locations)
				require.NoError(t, err)
				againWire, err := again.Marshal(t.Context())
				require.NoError(t, err)
				require.Equal(t, wire, againWire)
				for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
					request, err := again.Request(t.Context(), arm, "task")
					require.NoError(t, err)
					require.Equal(t, []byte(arm), request.Resources[0].Content)
					request.Resources[0].Content[0] = '!'
					spec, task, err := again.Task(t.Context(), arm, "task")
					require.NoError(t, err)
					spec.Name = "caller mutation"
					task.Stimulus.Message = "caller mutation"
				}
				againWire[0] = '!'
				unchanged, err := again.Marshal(t.Context())
				require.NoError(t, err)
				require.Equal(t, wire, unchanged, "public results cannot mutate retained sources or wire")
				require.NoError(t, again.Recheck(t.Context()))
			}
			write(t, base, "baseline/context/unused.txt", "not consumed")
			require.NoError(t, captured.Recheck(t.Context()), "unconsumed files remain alternative-valid")
			write(t, base, "baseline/context/a.txt", "modified")
			require.Error(t, captured.Recheck(t.Context()))
			unchanged, err := captured.Marshal(t.Context())
			require.NoError(t, err)
			require.Equal(t, wire, unchanged, "source mutation cannot change published bytes")
			changed, err := CaptureCollection(t.Context(), locations)
			require.NoError(t, err)
			changedWire, err := changed.Marshal(t.Context())
			require.NoError(t, err)
			require.NotEqual(t, wire, changedWire, "a fresh capture must observe changed source bytes")
			require.NoError(t, os.Remove(filepath.Join(base, "baseline/context/a.txt")))
			failed, err := CaptureCollection(t.Context(), locations)
			require.Error(t, err)
			require.Nil(t, failed)
		})
	}
}

func TestSourceRecordOnlyRetainsOwnedBytes(t *testing.T) {
	for _, content := range []string{"original", ""} {
		t.Run(map[bool]string{true: "empty", false: "populated"}[content == ""], func(t *testing.T) {
			files, base := reader(t, t.Context())
			path := filepath.Join(base, "input")
			write(t, base, "input", content)
			files.role = "executable"
			for range 2 {
				data, err := files.readFile(path, false)
				require.NoError(t, err)
				require.Nil(t, data, "record-only initial and cached reads must return no buffer")
				require.Equal(t, len(content), *files.total, "cached reads must not count bytes again")
			}
			require.Contains(t, files.data, "input")
			require.Equal(t, content, string(files.data["input"]))
			files.role = "request_input"
			for range 2 {
				data, err := files.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, content, string(data))
				if len(data) != 0 {
					data[0] = '!'
					require.Equal(t, content, string(files.data["input"]))
				} else {
					require.Nil(t, data)
				}
			}
			inventory := files.takeInventory()
			require.Len(t, inventory, 1)
			require.Equal(t, content, string(inventory[0].Bytes))
			require.Equal(t, []string{"executable", "request_input"}, inventory[0].Roles)
			require.Equal(t, releasepolicy.SourceDigest([]byte(content)), inventory[0].Digest)
			if content == "" {
				require.Nil(t, inventory[0].Bytes)
			}
			data, err := files.readFile(path, false)
			require.ErrorContains(t, err, "uncaptured input")
			require.Nil(t, data)
			require.ErrorIs(t, files.sticky, err)
		})
	}
}

func TestSourceRecordOnlyPreservesReadFailures(t *testing.T) {
	for _, name := range []string{"missing", "directory", "source_limit", "total_limit", "cancel", "changed_during_read", "physical_alias"} {
		t.Run(name, func(t *testing.T) {
			ctx := &readContext{Context: context.Background(), onCheck: func(int) error { return nil }}
			files, base := reader(t, ctx)
			files.role = "executable"
			write(t, base, "input", "original")
			want := ""
			switch name {
			case "missing":
				require.NoError(t, os.Remove(filepath.Join(base, "input")))
			case "directory":
				require.NoError(t, os.Remove(filepath.Join(base, "input")))
				require.NoError(t, os.Mkdir(filepath.Join(base, "input"), 0700))
				want = "not a regular file"
			case "source_limit":
				file, err := os.OpenFile(filepath.Join(base, "input"), os.O_WRONLY, 0)
				require.NoError(t, err)
				require.NoError(t, file.Truncate(maxExecutableBytes+1))
				require.NoError(t, file.Close())
				want = "source limit"
			case "total_limit":
				*files.total = maxTotalBytes
				want = "byte limit"
			case "cancel":
				ctx.onCheck = func(int) error { return context.Canceled }
			case "changed_during_read":
				ctx.onCheck = func(call int) error {
					if call == 6 {
						write(t, base, "input", "different length")
					}
					return nil
				}
				want = "changed during read"
			case "physical_alias":
				require.NoError(t, os.Link(filepath.Join(base, "input"), filepath.Join(base, "alias")))
				_, err := files.ReadFile(filepath.Join(base, "alias"))
				require.NoError(t, err)
				want = "physical source path alias"
			}
			data, err := files.readFile(filepath.Join(base, "input"), false)
			require.Error(t, err)
			switch name {
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
			case "missing":
				require.ErrorIs(t, err, fs.ErrNotExist)
			default:
				require.ErrorContains(t, err, want)
			}
			require.Nil(t, data)
			require.NotContains(t, files.data, "input")
			require.ErrorIs(t, files.sticky, err)
		})
	}
}

func TestCollectionCaptureExecutableRecordOnlyMatchesFrozenSource(t *testing.T) {
	for _, path := range []string{"evaluator", "baseline/eval.yaml", "baseline/context/a.txt"} {
		t.Run(path, func(t *testing.T) {
			base, locations := fixture(t)
			for arm, location := range locations {
				location.Executable = path
				locations[arm] = location
			}
			executable, err := os.ReadFile(filepath.Join(base, path))
			require.NoError(t, err)
			original := frozenOriginalCapture(t, locations)
			captured, err := CaptureCollection(t.Context(), locations)
			require.NoError(t, err)
			require.Equal(t, original.Canonical, captured.capture.prepared.canonical)
			require.Equal(t, original.Sources, captured.capture.prepared.sources)
			projections, err := captured.Projections(t.Context())
			require.NoError(t, err)
			require.Equal(t, original.Projection, projections)
			for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
				found := false
				for _, entry := range captured.capture.prepared.sources[arm] {
					if entry.Path == path {
						found = true
						require.Contains(t, entry.Roles, "executable")
						require.Equal(t, executable, entry.Bytes)
						require.Equal(t, releasepolicy.SourceDigest(executable), entry.Digest)
						if arm == releasepolicy.Baseline && path == "baseline/context/a.txt" {
							require.Contains(t, entry.Roles, "resource")
						}
						if arm == releasepolicy.Baseline && path == "baseline/eval.yaml" {
							require.Contains(t, entry.Roles, "eval")
						}
					}
				}
				require.True(t, found, "both arms must retain the executable source")
			}
			wire, err := captured.Marshal(t.Context())
			require.NoError(t, err)
			request, err := captured.Request(t.Context(), releasepolicy.Baseline, "task")
			require.NoError(t, err)
			require.Equal(t, []byte("baseline"), request.Resources[0].Content)
			request.Resources[0].Content[0] = '!'
			unchanged, err := captured.Marshal(t.Context())
			require.NoError(t, err)
			require.Equal(t, wire, unchanged, "a later cached public read must remain detached from executable bytes")
			require.NoError(t, captured.Recheck(t.Context()))

			changed := append([]byte(nil), executable...)
			changed[0] ^= 1
			require.NotEqual(t, executable, changed)
			require.NoError(t, os.WriteFile(filepath.Join(base, path), changed, 0600))
			require.Error(t, captured.Recheck(t.Context()), "same-size executable mutation must invalidate currentness")
			unchanged, err = captured.Marshal(t.Context())
			require.NoError(t, err)
			require.Equal(t, wire, unchanged, "source changes cannot mutate retained capture bytes")
		})
	}
}

func TestCollectionInitializationValidatesAndOwnsProjections(t *testing.T) {
	for _, mutation := range []string{"none", "source", "snapshot", "associations"} {
		t.Run(mutation, func(t *testing.T) {
			_, locations := fixture(t)
			capture, err := prepareProjectionCapture(collectionContext(t.Context()), locations)
			require.NoError(t, err)
			switch mutation {
			case "source":
				capture.prepared.sources[releasepolicy.Baseline][0].Bytes[0] ^= 1
			case "snapshot":
				capture.prepared.canonical[0] ^= 1
			case "associations":
				capture.associations.canonical[0] ^= 1
			}
			collection := &CollectionCapture{capture: capture}
			err = collection.initialize(t.Context())
			if mutation != "none" {
				require.Error(t, err)
				require.Nil(t, collection.projections)
				return
			}
			require.NoError(t, err)
			before, err := collection.Projections(t.Context())
			require.NoError(t, err)
			capture.prepared.sources[releasepolicy.Baseline][0].Bytes[0] ^= 1
			after, err := collection.Projections(t.Context())
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestRetainedExecutableReadOwnershipAndAdmission(t *testing.T) {
	for _, detached := range []bool{false, true} {
		for _, state := range []string{"valid", "canceled", "mismatched", "exhausted", "sticky"} {
			name := state
			if detached {
				name += "/detached"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				content := []byte("actual retained executable")
				index := uint32(0)
				reader := newRetainedArmInputs(ctx, t.TempDir(), armAssociations{
					Events: []associationEvent{{
						Ordinal: 0, Phase: executablePhase, Role: "executable",
						Op: readOperation, Path: "evaluator", Source: &index,
					}},
				}, []source{{Path: "evaluator", Bytes: content}})
				reader.set(executablePhase, 0, "executable")
				switch state {
				case "canceled":
					cancel()
				case "mismatched":
					reader.role = "request_input"
				case "exhausted":
					reader.position = 1
				case "sticky":
					reader.sticky = context.DeadlineExceeded
				}
				read := reader.readRetained
				if detached {
					read = reader.read
				}
				got, err := read("evaluator")
				if state != "valid" {
					require.Error(t, err)
					require.Nil(t, got)
					if state == "canceled" {
						require.ErrorIs(t, err, context.Canceled)
					} else {
						require.ErrorIs(t, reader.sticky, err)
					}
					return
				}
				require.NoError(t, err)
				require.Equal(t, content, got)
				require.Equal(t, 1, reader.position)
				if detached {
					require.NotSame(t, &content[0], &got[0])
					got[0] ^= 1
					require.Equal(t, byte('a'), content[0])
				} else {
					require.Same(t, &content[0], &got[0])
				}
			})
		}
	}
}

// BenchmarkCaptureCollectionActualExecutable measures source recapture using the
// real test executable under collection-level bounds. An explicit executable
// keeps the payload fixed when comparing different benchmark builds.
func BenchmarkCaptureCollectionActualExecutable(b *testing.B) {
	base, locations := fixture(b)
	executable := os.Getenv("WAZA_CAPTURE_BENCHMARK_EXECUTABLE")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		require.NoError(b, err)
	}
	payload, err := os.ReadFile(executable)
	require.NoError(b, err)
	digest := sha256.Sum256(payload)
	b.Logf("executable input: size=%d sha256=%s", len(payload), hex.EncodeToString(digest[:]))
	input, err := os.Open(executable)
	require.NoError(b, err)
	output, err := os.OpenFile(filepath.Join(base, "evaluator"), os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(b, err)
	_, copyErr := io.Copy(output, input)
	require.NoError(b, input.Close())
	require.NoError(b, copyErr)
	require.NoError(b, output.Close())

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		captured, err := CaptureCollection(b.Context(), locations)
		require.NoError(b, err)
		require.NotNil(b, captured)
	}
}
