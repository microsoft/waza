package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseFault struct {
	inner *fakeAzure
	alter func(*http.Response)
}

func (f responseFault) Do(request *http.Request) (*http.Response, error) {
	response, err := f.inner.Do(request)
	if err == nil && request.Method == http.MethodGet && request.URL.Query().Get("comp") != "list" {
		f.alter(response)
	}
	return response, err
}

type faultBody struct {
	io.Reader
	readErr  error
	closeErr error
	closed   bool
}

func (b *faultBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.Reader.Read(p)
}

func (b *faultBody) Close() error {
	b.closed = true
	return b.closeErr
}

func TestAzureNativeDownloadFailuresAreNotVerified(t *testing.T) {
	for _, mode := range []string{"permission", "length", "read", "close", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("original content")
			key := "blob-" + Hash(data)
			transport := &fakeAzure{objects: map[string][]byte{key: data}}
			body := &faultBody{Reader: bytes.NewReader(data)}
			injected := errors.New("injected " + mode)
			backend := azureBackend(t, responseFault{inner: transport, alter: func(response *http.Response) {
				response.Body = body
				switch mode {
				case "permission":
					response.StatusCode = http.StatusForbidden
					response.Header.Set("X-Ms-Error-Code", "AuthorizationFailure")
				case "length":
					response.Header.Del("Content-Length")
				case "read":
					body.readErr = injected
				case "close":
					body.closeErr = injected
				case "corrupt":
					body.Reader = strings.NewReader(strings.Repeat("x", len(data)))
				}
			}})
			got, err := backend.Get(context.Background(), key, int64(len(data)))
			if err == nil || got != nil || errors.Is(err, ErrMissing) {
				t.Fatalf("download failure became content/missing: %q %v", got, err)
			}
			if !body.closed {
				t.Fatal("failed download body was not closed")
			}
			if (mode == "read" || mode == "close") && !errors.Is(err, injected) {
				t.Fatalf("download lost failure cause: %v", err)
			}
		})
	}
}

func TestAzureNativeInterruptedConditionalCommitRecovers(t *testing.T) {
	backend, transport := azureFixture(t)
	store := NewStore(backend)
	m, data := publicationFixture(t)
	wire, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.Create(context.Background(), wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), m.Artifacts[0], data); err != nil {
		t.Fatal(err)
	}
	transport.failCommittedPut = errors.New("connection interrupted after server commit")
	receipt, err := store.Finalize(context.Background(), digest)
	if err == nil || receipt.Integrity == "verified" || receipt.Publication != "unknown" {
		t.Fatalf("lost Azure response became success: %+v %v", receipt, err)
	}
	receipt, err = store.Finalize(context.Background(), digest)
	if err != nil || receipt.Publication != "finalized" || receipt.Integrity != "verified" {
		t.Fatalf("conditional SDK recovery failed: %+v %v", receipt, err)
	}
}

func TestAzureConditionalUploadRecognizesOccupiedResponses(t *testing.T) {
	data := []byte("immutable content")
	key := "blob-" + Hash(data)
	for _, response := range []struct {
		name   string
		status int
		code   string
	}{
		{name: "condition-not-met", status: http.StatusPreconditionFailed, code: "ConditionNotMet"},
		{name: "blob-already-exists", status: http.StatusConflict, code: "BlobAlreadyExists"},
	} {
		t.Run(response.name, func(t *testing.T) {
			transport := &fakeAzure{
				objects:        map[string][]byte{key: data},
				occupiedStatus: response.status,
				occupiedCode:   response.code,
			}
			created, err := azureBackend(t, transport).Create(context.Background(), key, data)
			if created || err != nil {
				t.Fatalf("occupied response became creation/failure: %v %v", created, err)
			}
		})
	}
}

func TestAzurePublicationCloseFailuresPreserveObservedMarker(t *testing.T) {
	for _, object := range []string{"blob-", "final-"} {
		t.Run(object, func(t *testing.T) {
			transport := &fakeAzure{objects: make(map[string][]byte)}
			armed := false
			var body *faultBody
			injected := errors.New("injected response close failure")
			backend := azureBackend(t, responseFault{inner: transport, alter: func(response *http.Response) {
				if armed && strings.Contains(response.Request.URL.Path, "/"+object) {
					body = &faultBody{Reader: response.Body, closeErr: injected}
					response.Body = body
				}
			}})
			store := NewStore(backend)
			m, terminal := publicationFixture(t)
			wire, err := Encode(m)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := store.Create(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Put(context.Background(), m.Artifacts[0], terminal); err != nil {
				t.Fatal(err)
			}
			before, err := store.Finalize(context.Background(), digest)
			if err != nil || before.Publication != "finalized" || before.Integrity != "verified" {
				t.Fatalf("control did not finalize actual content: %+v %v", before, err)
			}
			armed = true
			receipt, err := store.Inspect(context.Background(), digest)
			want := "finalized"
			if object == "final-" {
				want = "unknown"
			}
			if !errors.Is(err, injected) || receipt.Publication != want || receipt.Integrity != "failed" ||
				body == nil || !body.closed {
				t.Fatalf("failed close invented or erased marker observation: %+v %v", receipt, err)
			}
			completed, err := store.ReadCompleted(context.Background(), digest)
			if !errors.Is(err, injected) || completed != nil {
				t.Fatalf("failed close returned completed source: %+v %v", completed, err)
			}
		})
	}
}
