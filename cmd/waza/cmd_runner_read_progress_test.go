package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	goformat "go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const runnerReadProgressSize = (2 << 20) + 1
const runnerReadProgressPrefix = "WAZA_TEST_STDIN_PROGRESS/"
const runnerReadProgressOriginalSHA256 = "ec26f3f332c3f114445c6641b2feda5c34bda87fad0a6f3c345eb9e4e98d723e"

// Sharing OS-independent process ownership lets portable fixtures run
// the same sole-Wait/copy/cleanup path as the native Windows controller.
type runnerWindowsMessage struct {
	Kind                string                  `json:"kind"`
	Handle              uint64                  `json:"handle,omitempty"`
	Binary              string                  `json:"binary,omitempty"`
	Action              string                  `json:"action,omitempty"`
	PID                 uint32                  `json:"pid,omitempty"`
	NoDeadline          bool                    `json:"no_deadline,omitempty"`
	GoVersion           string                  `json:"go_version,omitempty"`
	Code                int                     `json:"code,omitempty"`
	Stdout              []byte                  `json:"stdout,omitempty"`
	Stderr              []byte                  `json:"stderr,omitempty"`
	StartupDeadline     time.Time               `json:"startup_deadline,omitempty"`
	Readiness           *runnerConsoleReadiness `json:"readiness,omitempty"`
	ProgressNonce       string                  `json:"progress_nonce,omitempty"`
	ConsumptionDeadline time.Time               `json:"consumption_deadline,omitempty"`
	ExpectedBytes       int                     `json:"expected_bytes,omitempty"`
	ExpectedSHA256      string                  `json:"expected_sha256,omitempty"`
	Progress            *runnerReadProgress     `json:"progress,omitempty"`
	RawStderr           []byte                  `json:"raw_stderr,omitempty"`
}

type runnerWindowsOutput struct {
	mu      sync.Mutex
	data    []byte
	framing *runnerHelperFraming
	events  chan runnerWindowsMessage
}

func (w *runnerWindowsOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.data)+len(data) > 1<<20 {
		return 0, errors.New("Windows runner process output exceeded 1 MiB")
	}
	w.data = append(w.data, data...)
	if w.events != nil {
		records, err := w.framing.write(data)
		if err != nil {
			return 0, fmt.Errorf("framing Windows controller output: %w", err)
		}
		for _, line := range records {
			var event runnerWindowsMessage
			if err := json.Unmarshal(line, &event); err != nil {
				return 0, fmt.Errorf("decoding Windows controller event: %w", err)
			}
			select {
			case w.events <- event:
			default:
				return 0, errors.New("Windows controller event queue overflow")
			}
		}
	}
	return len(data), nil
}

func (w *runnerWindowsOutput) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.framing != nil {
		return w.framing.finish()
	}
	return nil
}

func (w *runnerWindowsOutput) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.data)
}

type runnerWindowsProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	copyErr  error
	stdout   *runnerWindowsOutput
	stderr   *runnerWindowsOutput
	progress *runnerReadProgressStream
}

func startRunnerWindowsProcess(cmd *exec.Cmd, protocol bool, progressNonce ...string) (*runnerWindowsProcess, error) {
	p := &runnerWindowsProcess{
		cmd: cmd, done: make(chan struct{}),
		stdout: &runnerWindowsOutput{}, stderr: &runnerWindowsOutput{},
	}
	if protocol {
		p.stdout.events = make(chan runnerWindowsMessage, 16)
		p.stdout.framing = newRunnerHelperFraming("ready", "armed", "capability", "started", "consumed", "result", "finished")
	}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if len(progressNonce) != 0 {
		p.progress = newRunnerReadProgressStream(progressNonce[0], p.stderr)
		cmd.Stderr = p.progress
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		defer close(p.done)
		waitErr := cmd.Wait()
		var progressErr error
		if p.progress != nil {
			progressErr = p.progress.finish()
		}
		p.copyErr = errors.Join(p.stdout.finish(), progressErr)
		p.err = errors.Join(waitErr, p.copyErr)
	}()
	return p, nil
}

func (p *runnerWindowsProcess) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for Windows process %d: %w", p.cmd.Process.Pid, ctx.Err())
	}
}

func (p *runnerWindowsProcess) stop() error {
	select {
	case <-p.done:
		return p.copyErr
	default:
	}
	killErr := p.cmd.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitErr := p.wait(ctx)
	if _, exited := errors.AsType[*exec.ExitError](waitErr); exited {
		waitErr = p.copyErr
	}
	return errors.Join(killErr, waitErr)
}

type runnerReadProgress struct {
	Version    int    `json:"version"`
	Nonce      string `json:"nonce"`
	PID        uint32 `json:"pid"`
	Action     string `json:"action"`
	NoDeadline bool   `json:"no_deadline"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
}

func (r runnerReadProgress) validate(nonce string, pid uint32, action string, size int, digest string) error {
	if r.Version != 1 || r.Nonce != nonce || !runnerProgressNonce(nonce) || r.PID == 0 || r.PID != pid ||
		(r.Action != "validate" && r.Action != "run") || r.Action != action || !r.NoDeadline ||
		r.Bytes != runnerReadProgressSize || r.Bytes != size || len(r.SHA256) != 64 ||
		strings.Trim(r.SHA256, "0123456789abcdef") != "" || r.SHA256 != digest {
		return errors.New("invalid source-bound stdin read-progress identity, capability, bytes or digest")
	}
	return nil
}

func runnerProgressNonce(nonce string) bool {
	return len(nonce) == 32 && strings.Trim(nonce, "0123456789abcdef") == ""
}

type runnerReadProgressStream struct {
	mu       sync.Mutex
	raw      io.Writer
	nonce    string
	pending  []byte
	cli      []byte
	proof    runnerReadProgress
	seen     bool
	notified bool
	err      error
	ready    chan struct{}
}

func newRunnerReadProgressStream(nonce string, raw io.Writer) *runnerReadProgressStream {
	return &runnerReadProgressStream{nonce: nonce, raw: raw, ready: make(chan struct{})}
}

func (s *runnerReadProgressStream) notify() {
	if !s.notified {
		s.notified = true
		close(s.ready)
	}
}

func (s *runnerReadProgressStream) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.raw.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		s.err = errors.Join(s.err, fmt.Errorf("capturing read-progress raw stderr: %w", err))
		s.notify()
		return n, s.err
	}
	s.pending = append(s.pending, data...)
	for s.err == nil {
		end := bytes.IndexByte(s.pending, '\n')
		if end < 0 {
			if len(s.pending) > 512 && bytes.HasPrefix(s.pending, []byte(runnerReadProgressPrefix)) {
				s.err = errors.New("stdin read-progress frame exceeded 512 bytes")
			}
			break
		}
		line := s.pending[:end]
		s.pending = s.pending[end+1:]
		if !bytes.HasPrefix(line, []byte(runnerReadProgressPrefix)) {
			s.cli = append(s.cli, line...)
			s.cli = append(s.cli, '\n')
			continue
		}
		prefix := runnerReadProgressPrefix + s.nonce + " "
		if s.seen || len(line)+1 > 512 || !bytes.HasPrefix(line, []byte(prefix)) {
			s.err = errors.New("duplicate, oversized or wrong-nonce stdin read-progress frame")
			break
		}
		payload := line[len(prefix):]
		var proof runnerReadProgress
		if err := json.Unmarshal(payload, &proof); err != nil {
			s.err = fmt.Errorf("decoding stdin read-progress: %w", err)
			break
		}
		canonical, err := json.Marshal(proof)
		if err != nil {
			s.err = err
			break
		}
		if !bytes.Equal(canonical, payload) {
			s.err = errors.New("noncanonical or unknown/duplicate fields in stdin read-progress")
			break
		}
		if err := proof.validate(s.nonce, proof.PID, proof.Action, proof.Bytes, proof.SHA256); err != nil {
			s.err = err
			break
		}
		s.proof, s.seen = proof, true
	}
	if s.err != nil || s.seen {
		s.notify()
	}
	if s.err != nil {
		return len(data), s.err
	}
	return len(data), nil
}

func (s *runnerReadProgressStream) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) != 0 {
		if bytes.HasPrefix(s.pending, []byte(runnerReadProgressPrefix)) {
			s.err = errors.Join(s.err, errors.New("truncated stdin read-progress frame"))
		} else {
			s.cli = append(s.cli, s.pending...)
		}
		s.pending = nil
	}
	if !s.seen {
		s.err = errors.Join(s.err, errors.New("missing source-bound stdin read-progress"))
	}
	s.notify()
	return s.err
}

func (s *runnerReadProgressStream) result() (runnerReadProgress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen && s.err == nil {
		return runnerReadProgress{}, errors.New("stdin consumption has not been observed")
	}
	return s.proof, s.err
}

func (s *runnerReadProgressStream) cliSnapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Clone(s.cli)
}

func waitRunnerReadProgress(ctx context.Context, stream *runnerReadProgressStream, done <-chan struct{}) (runnerReadProgress, error) {
	if err := runnerConsoleContextError(ctx); err != nil {
		return runnerReadProgress{}, err
	}
	select {
	case <-stream.ready:
	case <-done:
		return runnerReadProgress{}, errors.New("target exited before consumption admission")
	case <-ctx.Done():
		return runnerReadProgress{}, ctx.Err()
	}
	if err := runnerConsoleContextError(ctx); err != nil {
		return runnerReadProgress{}, err
	}
	return stream.result()
}

const runnerObservedStdinSource = `
type runnerObservedStdin struct {
	file *os.File
	nonce string
	noDeadline bool
	total int
	digest hash.Hash
	reported bool
}

func (r *runnerObservedStdin) deadline(err error) error {
	r.noDeadline = errors.Is(err, os.ErrNoDeadline)
	return err
}

func (r *runnerObservedStdin) Read(p []byte) (int, error) {
	n, err := r.file.Read(p)
	if n > 0 {
		r.total += n
		_, hashErr := r.digest.Write(p[:n])
		if hashErr != nil {
			return n, errors.Join(err, fmt.Errorf("test stdin progress hashing: %w", hashErr))
		}
		if r.total > 2097153 {
			return n, errors.Join(err, errors.New("test stdin progress exceeded exact payload"))
		}
		if r.total == 2097153 && err == nil && !r.reported {
			r.reported = true
			if len(os.Args) != 3 || os.Args[1] != "runner" || (os.Args[2] != "validate" && os.Args[2] != "run") {
				return n, errors.Join(err, errors.New("test stdin progress requires exact runner action"))
			}
			record := struct {
				Version int ` + "`json:\"version\"`" + `
				Nonce string ` + "`json:\"nonce\"`" + `
				PID uint32 ` + "`json:\"pid\"`" + `
				Action string ` + "`json:\"action\"`" + `
				NoDeadline bool ` + "`json:\"no_deadline\"`" + `
				Bytes int ` + "`json:\"bytes\"`" + `
				SHA256 string ` + "`json:\"sha256\"`" + `
			}{1, r.nonce, uint32(os.Getpid()), os.Args[2], r.noDeadline, r.total, fmt.Sprintf("%x", r.digest.Sum(nil))}
			data, marshalErr := json.Marshal(record)
			if marshalErr != nil {
				return n, errors.Join(err, fmt.Errorf("test stdin progress encoding: %w", marshalErr))
			}
			frame := []byte("WAZA_TEST_STDIN_PROGRESS/" + r.nonce + " " + string(data) + "\n")
			written, writeErr := os.Stderr.Write(frame)
			if writeErr == nil && written != len(frame) {
				writeErr = io.ErrShortWrite
			}
			if writeErr != nil {
				return n, errors.Join(err, fmt.Errorf("test stdin progress publication: %w", writeErr))
			}
		}
	}
	return n, err
}
`

func runnerProgressAST(source []byte) ([]byte, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "source.go", source, 0)
	if err != nil {
		return nil, err
	}
	var result bytes.Buffer
	err = ast.Fprint(&result, nil, file, func(name string, value reflect.Value) bool {
		return value.Type() != reflect.TypeFor[token.Pos]() && name != "Obj" && name != "Scope" && name != "Unresolved"
	})
	return result.Bytes(), err
}

func runnerReadProgressOverlay(source []byte, nonce string) ([]byte, error) {
	if fmt.Sprintf("%x", sha256.Sum256(source)) != runnerReadProgressOriginalSHA256 {
		return nil, errors.New("inherited-reader source changed; review and rebind the test-only overlay")
	}
	if !runnerProgressNonce(nonce) {
		return nil, errors.New("read-progress overlay requires a 32-character lowercase hex nonce")
	}
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, "runner_stdin_windows.go", source, 0)
	if err != nil {
		return nil, err
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "readInheritedRunnerInput" {
			if body != nil {
				return nil, errors.New("duplicate inherited reader")
			}
			body = fn.Body
		}
	}
	if body == nil || len(body.List) != 6 {
		return nil, errors.New("unexpected inherited-reader body shape")
	}
	initial, ok := body.List[0].(*ast.IfStmt)
	if !ok {
		return nil, errors.New("missing initial stdin deadline probe")
	}
	assignment, ok := initial.Init.(*ast.AssignStmt)
	if !ok || len(assignment.Rhs) != 1 {
		return nil, errors.New("unexpected initial deadline assignment")
	}
	slice := func(n ast.Node) string {
		return string(source[positions.Position(n.Pos()).Offset:positions.Position(n.End()).Offset])
	}
	if slice(assignment.Rhs[0]) != "os.Stdin.SetReadDeadline(time.Time{})" ||
		slice(initial.Cond) != "err == nil" {
		return nil, errors.New("unexpected initial deadline expression")
	}
	read, ok := body.List[2].(*ast.AssignStmt)
	if !ok || len(read.Rhs) != 1 || slice(read.Rhs[0]) != "io.ReadAll(io.LimitReader(os.Stdin, runnercontract.MaxRequestBytes+1))" {
		return nil, errors.New("unexpected inherited ReadAll/LimitReader expression")
	}
	var encoded bytes.Buffer
	if err := goformat.Node(&encoded, positions, file); err != nil {
		return nil, err
	}
	text := encoded.String()
	declaration := fmt.Sprintf("\n\tprogress := &runnerObservedStdin{file: os.Stdin, nonce: %q, digest: sha256.New()}", nonce)
	signature := "func readInheritedRunnerInput(ctx context.Context) (data []byte, resultErr error) {"
	if strings.Count(text, signature) != 1 {
		return nil, errors.New("unexpected inherited-reader signature")
	}
	text = strings.Replace(text, signature, signature+declaration, 1)
	text = strings.Replace(text, "err := os.Stdin.SetReadDeadline(time.Time{})", "err := progress.deadline(os.Stdin.SetReadDeadline(time.Time{}))", 1)
	text = strings.Replace(text, "io.ReadAll(io.LimitReader(os.Stdin, runnercontract.MaxRequestBytes+1))",
		"io.ReadAll(io.LimitReader(progress, runnercontract.MaxRequestBytes+1))", 1)
	text = strings.Replace(text, "import (", "import (\n\"crypto/sha256\"\n\"encoding/json\"\n\"hash\"", 1)
	return goformat.Source([]byte(text + runnerObservedStdinSource))
}

func validateRunnerReadProgressOverlay(original, overlay []byte, nonce string) ([]byte, error) {
	expected, err := runnerReadProgressOverlay(original, nonce)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(overlay, expected) {
		return nil, errors.New("overlay bytes differ from the exact permitted instrumentation")
	}
	expectedAST, err := runnerProgressAST(expected)
	if err != nil {
		return nil, err
	}
	actualAST, err := runnerProgressAST(overlay)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(actualAST, expectedAST) {
		return nil, errors.New("overlay differs from the exact permitted instrumentation")
	}
	const helperBoundary = "\ntype runnerObservedStdin struct"
	if bytes.Count(overlay, []byte(helperBoundary)) != 1 {
		return nil, errors.New("inverse instrumentation lacks one helper boundary")
	}
	restored, _, _ := strings.Cut(string(overlay), helperBoundary)
	for _, undo := range []struct{ from, to string }{
		{fmt.Sprintf("\n\tprogress := &runnerObservedStdin{file: os.Stdin, nonce: %q, digest: sha256.New()}", nonce), ""},
		{"progress.deadline(os.Stdin.SetReadDeadline(time.Time{}))", "os.Stdin.SetReadDeadline(time.Time{})"},
		{"io.ReadAll(io.LimitReader(progress, runnercontract.MaxRequestBytes+1))", "io.ReadAll(io.LimitReader(os.Stdin, runnercontract.MaxRequestBytes+1))"},
		{"\t\"crypto/sha256\"\n", ""}, {"\t\"encoding/json\"\n", ""}, {"\t\"hash\"\n", ""},
	} {
		if strings.Count(restored, undo.from) != 1 {
			return nil, errors.New("inverse instrumentation lacks one exact substitution")
		}
		restored = strings.Replace(restored, undo.from, undo.to, 1)
	}
	inverse, err := runnerProgressAST([]byte(restored))
	if err != nil {
		return nil, err
	}
	originalAST, err := runnerProgressAST(original)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(inverse, originalAST) {
		return nil, errors.New("inverse instrumentation failed to restore the complete original AST")
	}
	return inverse, nil
}

func buildRunnerReadProgressCLI(t *testing.T) (string, string) {
	t.Helper()
	source, err := os.ReadFile("runner_stdin_windows.go")
	require.NoError(t, err)
	var random [16]byte
	_, err = rand.Read(random[:])
	require.NoError(t, err)
	nonce := fmt.Sprintf("%x", random)
	overlay, err := runnerReadProgressOverlay(source, nonce)
	require.NoError(t, err)
	inverse, err := validateRunnerReadProgressOverlay(source, overlay, nonce)
	require.NoError(t, err)
	directory := t.TempDir()
	replacement := filepath.Join(directory, "runner_stdin_windows.go")
	require.NoError(t, os.WriteFile(replacement, overlay, 0o600))
	original, err := filepath.Abs("runner_stdin_windows.go")
	require.NoError(t, err)
	manifest, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{original: replacement}})
	require.NoError(t, err)
	manifestPath := filepath.Join(directory, "overlay.json")
	require.NoError(t, os.WriteFile(manifestPath, manifest, 0o600))
	binary := filepath.Join(directory, "waza.exe")
	args := []string{"build", "-overlay=" + manifestPath, "-o", binary, "."}
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Env = append(os.Environ(), "GOOS=windows")
	if runtime.GOOS != "windows" {
		command.Env = append(command.Env, "CGO_ENABLED=0")
	}
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	current, err := os.ReadFile(original)
	require.NoError(t, err)
	require.Equal(t, source, current, "building an overlay must not edit the production file")
	t.Logf("instrumented-source-bound target: original_sha256=%x overlay_sha256=%x inverse_ast_sha256=%x helper_sha256=%x nonce=%s; go %q; overlay_source=%q; manifest=%s",
		sha256.Sum256(source), sha256.Sum256(overlay), sha256.Sum256(inverse), sha256.Sum256([]byte(runnerObservedStdinSource)), nonce, args, overlay, manifest)
	return binary, nonce
}

func TestRunnerTerminalReadProgressOverlayBuildOnly(t *testing.T) {
	binary, _ := buildRunnerReadProgressCLI(t)
	image, err := pe.Open(binary)
	require.NoError(t, err)
	defer func() { require.NoError(t, image.Close()) }()
	var subsystem uint16
	switch header := image.OptionalHeader.(type) {
	case *pe.OptionalHeader32:
		subsystem = header.Subsystem
	case *pe.OptionalHeader64:
		subsystem = header.Subsystem
	default:
		t.Fatal("overlay build lacks a supported PE optional header")
	}
	require.Equal(t, uint16(pe.IMAGE_SUBSYSTEM_WINDOWS_CUI), subsystem)
	data, err := os.ReadFile(binary)
	require.NoError(t, err)
	t.Logf("BUILD ONLY: source-bound Windows overlay compiled, PE machine=%d subsystem=%d sha256=%x; no Windows execution", image.Machine, subsystem, sha256.Sum256(data))
}

func TestRunnerTerminalReadProgressOverlay(t *testing.T) {
	source, err := os.ReadFile("runner_stdin_windows.go")
	require.NoError(t, err)
	nonce := strings.Repeat("a", 32)
	overlay, err := runnerReadProgressOverlay(source, nonce)
	require.NoError(t, err)
	_, err = validateRunnerReadProgressOverlay(source, overlay, nonce)
	require.NoError(t, err)
	for _, change := range []struct{ old, replacement string }{
		{"r.file.Read(p)", "0, io.EOF"},
		{"r.total == 2097153", "r.total > 0"},
		{"os.Stdin.Close()", "nil"},
		{"runnercontract.MaxRequestBytes+1", "runnercontract.MaxRequestBytes"},
		{"progress.deadline(os.Stdin.SetReadDeadline(time.Time{}))", "os.Stdin.SetReadDeadline(time.Time{})"},
		{"func (r *runnerObservedStdin) Read", "//go:nosplit\nfunc (r *runnerObservedStdin) Read"},
	} {
		t.Run(change.old, func(t *testing.T) {
			mutant := bytes.Replace(overlay, []byte(change.old), []byte(change.replacement), 1)
			require.NotEqual(t, overlay, mutant)
			_, err := validateRunnerReadProgressOverlay(source, mutant, nonce)
			require.Error(t, err)
		})
	}
	for _, invalid := range []string{"", strings.Repeat("A", 32), strings.Repeat("a", 31)} {
		_, err := runnerReadProgressOverlay(source, invalid)
		require.Error(t, err)
	}
	changedSource := bytes.Replace(source, []byte("os.Stdin.Close()"), []byte("os.Stdout.Close()"), 1)
	require.NotEqual(t, source, changedSource)
	_, err = runnerReadProgressOverlay(changedSource, nonce)
	require.ErrorContains(t, err, "source changed")
}

func TestRunnerTerminalReadProgressStream(t *testing.T) {
	nonce := strings.Repeat("a", 32)
	payload := append(bytes.Repeat([]byte(" "), runnerReadProgressSize-1), '{')
	proof := runnerReadProgress{1, nonce, 42, "validate", true, len(payload), fmt.Sprintf("%x", sha256.Sum256(payload))}
	encoded, err := json.Marshal(proof)
	require.NoError(t, err)
	frame := runnerReadProgressPrefix + nonce + " " + string(encoded) + "\n"
	t.Run("raw-short-write", func(t *testing.T) {
		stream := newRunnerReadProgressStream(nonce, &shortTerminalWriter{})
		_, err := stream.Write([]byte(frame))
		require.ErrorIs(t, err, io.ErrShortWrite)
		_, err = stream.result()
		require.ErrorIs(t, err, io.ErrShortWrite)
	})
	t.Run("raw-write-error", func(t *testing.T) {
		reader, writer := io.Pipe()
		require.NoError(t, reader.Close())
		defer func() { require.NoError(t, writer.Close()) }()
		stream := newRunnerReadProgressStream(nonce, writer)
		_, err := stream.Write([]byte(frame))
		require.ErrorIs(t, err, io.ErrClosedPipe)
		_, err = stream.result()
		require.ErrorIs(t, err, io.ErrClosedPipe)
	})
	for _, control := range []struct {
		name, input string
		valid       bool
	}{
		{"valid", frame + "runner input interrupted: context canceled\n", true},
		{"fragmented", frame, true},
		{"partial", frame[:len(frame)-1], false},
		{"duplicate", frame + frame, false},
		{"wrong-nonce", strings.Replace(frame, nonce, strings.Repeat("b", 32), 1), false},
		{"wrong-pid", strings.Replace(frame, `"pid":42`, `"pid":0`, 1), false},
		{"wrong-capability", strings.Replace(frame, `"no_deadline":true`, `"no_deadline":false`, 1), false},
		{"partial-bytes", strings.Replace(frame, `"bytes":2097153`, `"bytes":1`, 1), false},
		{"duplicate-field", strings.Replace(frame, `"pid":42`, `"pid":42,"pid":42`, 1), false},
		{"unknown-field", strings.Replace(frame, `"pid":42`, `"pid":42,"extra":1`, 1), false},
		{"malformed", runnerReadProgressPrefix + nonce + " {}\n", false},
		{"oversize", runnerReadProgressPrefix + strings.Repeat("a", 600), false},
		{"absent", "original diagnostic without frame", false},
	} {
		t.Run(control.name, func(t *testing.T) {
			for _, chunk := range []int{1, 7, len(control.input)} {
				var raw bytes.Buffer
				stream := newRunnerReadProgressStream(nonce, &raw)
				var writeErr error
				for offset := 0; offset < len(control.input) && writeErr == nil; offset += chunk {
					_, writeErr = stream.Write([]byte(control.input[offset:min(offset+chunk, len(control.input))]))
				}
				err := errors.Join(writeErr, stream.finish())
				if !control.valid {
					require.Error(t, err)
					require.NotEmpty(t, raw.Bytes(), "failed frames must retain original stderr")
					continue
				}
				require.NoError(t, err)
				actual, err := stream.result()
				require.NoError(t, err)
				require.Equal(t, proof, actual)
				require.Equal(t, control.input, raw.String())
				require.Equal(t, strings.TrimPrefix(control.input, frame), string(stream.cliSnapshot()))
				for _, negative := range []struct {
					pid    uint32
					action string
					digest string
				}{{420, "validate", proof.SHA256}, {42, "run", proof.SHA256}, {42, "validate", strings.Repeat("0", 64)}} {
					require.Error(t, actual.validate(nonce, negative.pid, negative.action, len(payload), negative.digest))
				}
			}
		})
	}
}

func TestRunnerTerminalReadProgressWait(t *testing.T) {
	for _, reason := range []string{"cancel", "deadline", "exit"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			stream := newRunnerReadProgressStream(strings.Repeat("a", 32), &bytes.Buffer{})
			done := make(chan struct{})
			if reason == "cancel" {
				cancel()
			}
			if reason == "exit" {
				close(done)
			}
			_, err := waitRunnerReadProgress(ctx, stream, done)
			require.Error(t, err)
		})
	}
}

func TestRunnerTerminalReadProgressProcess(t *testing.T) {
	const marker = "waza-source-progress-framing-fixture"
	nonce := strings.Repeat("a", 32)
	if len(os.Args) >= 3 && os.Args[len(os.Args)-2] == marker {
		proof := runnerReadProgress{1, nonce, 42, "validate", true, runnerReadProgressSize, strings.Repeat("b", 64)}
		data, err := json.Marshal(proof)
		require.NoError(t, err)
		frame := runnerReadProgressPrefix + nonce + " " + string(data) + "\n"
		switch os.Args[len(os.Args)-1] {
		case "duplicate", "duplicate-exit":
			frame += frame
		case "partial":
			frame = strings.TrimSuffix(frame, "\n")
		case "malformed":
			frame = runnerReadProgressPrefix + nonce + " {}\n"
		}
		_, err = io.WriteString(os.Stderr, frame)
		require.NoError(t, err)
		_, err = io.WriteString(os.Stdout, runnerHelperFixtureRecord)
		require.NoError(t, err)
		if os.Args[len(os.Args)-1] == "duplicate-exit" {
			t.Error("authored nonzero exit control")
		}
		return
	}
	for _, control := range []string{"valid", "duplicate", "duplicate-exit", "partial", "malformed"} {
		t.Run(control, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerTerminalReadProgressProcess$", "--", marker, control)
			command.WaitDelay = time.Second
			process, err := startRunnerWindowsProcess(command, false, nonce)
			require.NoError(t, err)
			t.Cleanup(func() {
				err := process.stop()
				if control == "valid" {
					require.NoError(t, err)
				} else {
					require.Error(t, err, "cleanup must retain joined parser/copy failure")
				}
			})
			waitErr := process.wait(ctx)
			require.NoError(t, ctx.Err(), "real process/copy must join before deadline")
			if control != "valid" {
				require.Error(t, waitErr)
				require.NotEmpty(t, process.stderr.snapshot())
				if control == "duplicate-exit" {
					_, exited := errors.AsType[*exec.ExitError](waitErr)
					require.True(t, exited, "retain process failure as well as parser failure")
					require.ErrorContains(t, waitErr, "duplicate")
				}
				return
			}
			require.NoError(t, waitErr)
			_, err = decodeRunnerHelperSnapshot(process.stdout.snapshot(), "portable-fixture")
			require.NoError(t, err)
			proof, err := process.progress.result()
			require.NoError(t, err)
			require.Equal(t, uint32(42), proof.PID)
			require.Empty(t, process.progress.cliSnapshot())
			t.Logf("actual native-controller process owner/sole-Wait/stderr-copy joined using real current binary; proof is nonnative authored data, covermode=%q raw_sha256=%x", testing.CoverMode(), sha256.Sum256(process.stderr.snapshot()))
		})
	}
}

func TestRunnerTerminalReadProgressActualReader(t *testing.T) {
	source, err := os.ReadFile("runner_stdin_windows.go")
	require.NoError(t, err)
	overlay, err := runnerReadProgressOverlay(source, strings.Repeat("a", 32))
	require.NoError(t, err)
	_, err = validateRunnerReadProgressOverlay(source, overlay, strings.Repeat("a", 32))
	require.NoError(t, err)
	dir := t.TempDir()
	replacements := strings.NewReplacer(`"os"`, `os "progressfixture/fakeos"`,
		`"github.com/microsoft/waza/internal/runnercontract"`, `runnercontract "progressfixture/contract"`,
		`"github.com/microsoft/waza/internal/runnerterminal"`, `runnerterminal "progressfixture/terminal"`)
	for name, content := range map[string]string{
		"go.mod":         "module progressfixture\n\ngo 1.26\n",
		"reader.go":      replacements.Replace(string(overlay)),
		"reader_test.go": runnerProgressReaderTests,
		"fakeos/os.go":   runnerProgressFakeOS,
		"contract/go.go": "package contract\nconst MaxRequestBytes=4<<20\n",
		"terminal/go.go": "package terminal\nimport \"io\"\ntype Envelope struct{}\ntype Binding struct{}\ntype Emitter struct{}\nfunc NewEmitter(io.Writer)*Emitter{return &Emitter{}}\nfunc(*Emitter)Emit(Envelope,*Binding)(int,error){return 0,nil}\n",
	} {
		file := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
	}
	args := []string{"test", "-count=1", "-json", "-timeout=30s"}
	info, ok := debug.ReadBuildInfo()
	require.True(t, ok)
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			args = append(args, "-race")
		}
	}
	if testing.CoverMode() != "" {
		args = append(args, "-covermode=atomic", "-coverprofile="+filepath.Join(dir, "reader.cover"))
	}
	args = append(args, ".")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	for _, name := range []string{"actual-caller", "canceled-reader-joins-close", "no-partial-progress", "zero-read",
		"bytes-and-error", "EOF-not-admitted", "short-progress-write", "failed-progress-write", "altered-bytes",
		"capability-preserved", "oversize", "wrong-action"} {
		require.Contains(t, string(output), `"Action":"pass","Package":"progressfixture","Test":"TestActualReader/`+name+`"`)
	}
	require.NotContains(t, string(output), `"Action":"skip"`)
	if testing.CoverMode() != "" {
		profile, err := os.ReadFile(filepath.Join(dir, "reader.cover"))
		require.NoError(t, err)
		require.Contains(t, string(profile), "mode: atomic\n")
		require.Contains(t, string(profile), "progressfixture/reader.go:")
		t.Logf("nested_reader_atomic_profile_hex=%x", profile)
	}
	// Encode nested output so cmd/go does not mistake its coverage summary for ours.
	t.Logf("exact generated reader/production caller fixture executed with substituted OS boundaries (not native), overlay_sha256=%x go=%q nested_output_hex=%x", sha256.Sum256(overlay), args, output)
}

const runnerProgressFakeOS = `package fakeos
import (
	"errors"
	"io"
	"sync"
	"time"
)
var ErrNoDeadline=errors.New("fixture no deadline")
var Args=[]string{"waza","runner","validate"}
type File struct {
	Reader io.Reader
	Writer io.Writer
	DeadlineError error
	CloseFunc func()error
	mu sync.Mutex
	Closes int
}
func(f *File)Read(p []byte)(int,error){return f.Reader.Read(p)}
func(f *File)Write(p []byte)(int,error){return f.Writer.Write(p)}
func(f *File)SetReadDeadline(time.Time)error{return f.DeadlineError}
func(f *File)SetWriteDeadline(time.Time)error{return nil}
func(f *File)Close()error{
	f.mu.Lock();f.Closes++;f.mu.Unlock()
	if f.CloseFunc!=nil{return f.CloseFunc()}
	return nil
}
var Stdin,Stderr,Stdout *File
func Getpid()int{return 42}
`

const runnerProgressReaderTests = `package main
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	os "progressfixture/fakeos"
)
type boundaryReader struct { fn func([]byte)(int,error) }
func(r boundaryReader)Read(p []byte)(int,error){return r.fn(p)}
type boundaryWriter struct { err error; short bool }
func(w boundaryWriter)Write(p []byte)(int,error){
	if w.err!=nil{return 0,w.err}
	if w.short{return len(p)-1,nil}
	return len(p),nil
}
func TestActualReader(t *testing.T) {
	payload:=append(bytes.Repeat([]byte(" "),2097152),'{')
	for _,name:=range []string{"actual-caller","canceled-reader-joins-close","no-partial-progress","zero-read",
		"bytes-and-error","EOF-not-admitted","short-progress-write","failed-progress-write","altered-bytes",
		"capability-preserved","oversize","wrong-action"} {
		t.Run(name,func(t *testing.T){
			var stderr bytes.Buffer
			os.Args=[]string{"waza","runner","validate"}
			os.Stderr=&os.File{Writer:&stderr}
			os.Stdout=&os.File{Writer:&bytes.Buffer{}}
			file:=&os.File{Reader:bytes.NewReader(payload),DeadlineError:os.ErrNoDeadline}
			os.Stdin=file
			if name=="actual-caller" {
				got,err:=readInheritedRunnerInput(t.Context())
				if err!=nil||!bytes.Equal(got,payload){t.Fatalf("actual caller bytes/error: %d %v",len(got),err)}
				if file.Closes!=0{t.Fatal("reader closed stdin without cancellation")}
			} else if name=="canceled-reader-joins-close" {
				entered,release,closed:=make(chan struct{}),make(chan struct{}),make(chan struct{})
				var releaseOnce sync.Once
				file.Reader=boundaryReader{func(p []byte)(int,error){close(entered);<-release;return 0,errors.New("closed stdin")}}
				file.CloseFunc=func()error{releaseOnce.Do(func(){close(release)});close(closed);return nil}
				ctx,cancel:=context.WithCancel(t.Context())
				done,finished:=make(chan error,1),make(chan struct{})
				go func(){defer close(finished);_,err:=readInheritedRunnerInput(ctx);done<-err}()
				defer func(){
					cancel()
					releaseOnce.Do(func(){close(release)})
					select {case <-finished:case <-time.After(time.Second):t.Error("owned reader did not join during fault cleanup")}
				}()
				select {case <-entered:case <-time.After(time.Second):t.Fatal("actual reader never entered")}
				cancel()
				select {
				case err:=<-done: if !errors.Is(err,context.Canceled){t.Fatalf("lost cancellation: %v",err)}
				case <-time.After(time.Second): t.Fatal("canceled actual reader did not join")
				}
				select {case <-closed: default:t.Fatal("actual caller returned before close callback")}
				if file.Closes!=1 || stderr.Len()!=0 {t.Fatal("close ownership/progress violation")}
				return
			} else {
				reader:=&runnerObservedStdin{file:file,nonce:strings.Repeat("a",32),digest:sha256.New()}
				probeErr:=os.ErrNoDeadline
				if name=="capability-preserved"{probeErr=nil}
				if reader.deadline(probeErr)!=probeErr{t.Fatal("deadline probe error changed")}
				data:=bytes.Clone(payload)
				sentinel:=errors.New("injected boundary error")
				switch name {
				case "no-partial-progress": data=data[:len(data)-1]
				case "zero-read": file.Reader=boundaryReader{func([]byte)(int,error){return 0,nil}}
				case "bytes-and-error","EOF-not-admitted":
					cause:=error(sentinel);if name=="EOF-not-admitted"{cause=io.EOF}
					file.Reader=boundaryReader{func(p []byte)(int,error){return copy(p,payload),cause}}
				case "short-progress-write": os.Stderr.Writer=boundaryWriter{short:true}
				case "failed-progress-write": os.Stderr.Writer=boundaryWriter{err:sentinel}
				case "altered-bytes": data[0]='x'
				case "oversize": data=append(data,'x')
				case "wrong-action": os.Args=[]string{"waza","runner","unknown"}
				}
				if name!="zero-read"&&name!="bytes-and-error"&&name!="EOF-not-admitted"{file.Reader=bytes.NewReader(data)}
				if name=="short-progress-write"||name=="failed-progress-write" {
					_,err:=readInheritedRunnerInput(t.Context())
					if err==nil||!strings.Contains(err.Error(),"reading runner request: test stdin progress publication:"){t.Fatalf("actual caller hid instrumentation failure: %v",err)}
					if name=="short-progress-write"&&!errors.Is(err,io.ErrShortWrite){t.Fatal("actual caller lost short write")}
					if name=="failed-progress-write"&&!errors.Is(err,sentinel){t.Fatal("actual caller lost write cause")}
					return
				}
				n,err:=reader.Read(make([]byte,len(payload)+1))
				if name=="short-progress-write"||name=="failed-progress-write"||name=="oversize"||name=="wrong-action" {
					if err==nil||!strings.Contains(err.Error(),"test stdin progress"){t.Fatalf("instrumentation failure hidden: %v",err)}
					if name=="short-progress-write"&&!errors.Is(err,io.ErrShortWrite){t.Fatal("lost short write")}
					if name=="failed-progress-write"&&!errors.Is(err,sentinel){t.Fatal("lost write cause")}
					return
				}
				if name=="bytes-and-error" {
					if n!=len(payload)||!errors.Is(err,sentinel)||stderr.Len()!=0{t.Fatal("changed returned bytes/error or admitted error read")}
					return
				}
				if name=="EOF-not-admitted" {
					if n!=len(payload)||!errors.Is(err,io.EOF)||stderr.Len()!=0{t.Fatal("EOF admitted")}
					return
				}
				if err!=nil{t.Fatal(err)}
				if name=="no-partial-progress"||name=="zero-read" {
					if stderr.Len()!=0{t.Fatal("progress before complete actual Read")}
					return
				}
			}
			lines:=strings.Split(strings.TrimSuffix(stderr.String(),"\n"),"\n")
			if len(lines)!=1{t.Fatalf("frames=%d",len(lines))}
			prefix:="WAZA_TEST_STDIN_PROGRESS/"+strings.Repeat("a",32)+" "
			if !strings.HasPrefix(lines[0],prefix){t.Fatal("missing bound frame")}
			var proof struct{PID uint32 ` + "`json:\"pid\"`" + `; Bytes int ` + "`json:\"bytes\"`" + `; SHA256 string ` + "`json:\"sha256\"`" + `; NoDeadline bool ` + "`json:\"no_deadline\"`" + `}
			if err:=json.Unmarshal([]byte(strings.TrimPrefix(lines[0],prefix)),&proof);err!=nil{t.Fatal(err)}
			expected:=fmt.Sprintf("%x",sha256.Sum256(payload))
			if name=="altered-bytes" {
				if proof.SHA256==expected{t.Fatal("echoed expected digest instead of observed bytes")}
			} else if proof.SHA256!=expected {t.Fatal("wrong observed digest")}
			if proof.Bytes!=len(payload)||proof.PID!=42{t.Fatal("wrong actual count/PID")}
			if proof.NoDeadline!=(name!="capability-preserved"){t.Fatal("deadline capability fabricated")}
		})
	}
}
`

func TestRunnerTerminalReadProgressControllerSource(t *testing.T) {
	source, err := os.ReadFile("cmd_runner_terminal_windows_test.go")
	require.NoError(t, err)
	require.NotContains(t, string(source), `"PeekNamedPipe"`, "a synchronous pipe query cannot run on the timeout/cleanup owner")
	for _, required := range []string{`case "observe":`, `"consumed"`, "ConsumptionDeadline", "runnerReadProgress"} {
		require.True(t, strings.Contains(string(source), required), "actual Windows controller lacks %s", required)
	}
}
