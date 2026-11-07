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
	if err == nil || receipt.Integrity == "verified" {
		t.Fatalf("lost Azure response became success: %+v %v", receipt, err)
	}
	receipt, err = store.Finalize(context.Background(), digest)
	if err != nil || receipt.Publication != "finalized" || receipt.Integrity != "verified" {
		t.Fatalf("conditional SDK recovery failed: %+v %v", receipt, err)
	}
}
