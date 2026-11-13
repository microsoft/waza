package executionlifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/artifactmanifest"
)

type offlineCredential struct{}

func (offlineCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// stallingAzure is an in-memory container whose creates block, ignoring the
// request context, until release is closed.
type stallingAzure struct {
	mu      sync.Mutex
	objects map[string][]byte
	release chan struct{}
}

func (b *stallingAzure) Do(request *http.Request) (*http.Response, error) {
	key := strings.TrimPrefix(request.URL.Path, "/container/")
	header := http.Header{"Etag": {`"test"`}}
	respond := func(status int, data []byte) (*http.Response, error) {
		header.Set("Content-Length", strconv.Itoa(len(data)))
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)),
			ContentLength: int64(len(data)), Request: request}, nil
	}
	if request.Method == http.MethodPut {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		<-b.release
		b.mu.Lock()
		defer b.mu.Unlock()
		b.objects[key] = data
		return respond(http.StatusCreated, nil)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.objects[key]
	if !ok {
		header.Set("X-Ms-Error-Code", "BlobNotFound")
		return respond(http.StatusNotFound, nil)
	}
	header.Set("X-Ms-Blob-Type", "BlockBlob")
	return respond(http.StatusOK, data)
}

func TestJournalBoundsStalledProviderWrites(t *testing.T) {
	fake := &stallingAzure{objects: map[string][]byte{}, release: make(chan struct{})}
	backend, err := artifactmanifest.NewAzure("https://offline.invalid/container", offlineCredential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: fake, Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, scope := testScope(t)
	journal, err := Open(backend, scope, allow)
	if err != nil {
		t.Fatal(err)
	}
	journal.bound = 20 * time.Millisecond
	begin := time.Now()
	if err := journal.Append(context.Background(), started(time.Second, at(time.Minute))); !errors.Is(err, ErrWriteStalled) {
		t.Fatalf("stalled write was not bounded: %v", err)
	}
	if err := journal.Append(context.Background(), Event{Event: Heartbeat, At: epoch}); !errors.Is(err, ErrWriteStalled) {
		t.Fatalf("journal continued after a stalled write: %v", err)
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("caller waited %s for stalled I/O", elapsed)
	}
	if err := journal.Wait(10 * time.Millisecond); !errors.Is(err, ErrWriteStalled) {
		t.Fatalf("Wait hid the in-flight write: %v", err)
	}
	close(fake.release)
	if err := journal.Wait(5 * time.Second); err != nil {
		t.Fatalf("abandoned write did not drain: %v", err)
	}
}

func TestJournalWritesRequireCurrentOwnerAuthority(t *testing.T) {
	ctx := context.Background()
	backend, store, scope := testScope(t)
	denied := errors.New("creator replaced")
	journal, err := Open(backend, scope, func(context.Context) error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(ctx, started(time.Second, at(time.Minute))); !errors.Is(err, ErrUnauthorized) || !errors.Is(err, denied) {
		t.Fatalf("write without current authority was admitted: %v", err)
	}
	if _, err := Open(backend, scope, nil); err == nil {
		t.Fatal("journal opened without owner authority")
	}
	// The read-only store cannot seed the journal from public scope IDs.
	owned, err := store.Scope(scope)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeEvent(started(time.Second, at(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := owned.PrepareStart(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owned.Commit(ctx, pending); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("read-only store admitted a write: %v", err)
	}
	observed, err := Read(ctx, store, scope, nil)
	if err != nil || observed.Invalid || len(observed.Events) != 0 {
		t.Fatalf("denied writes left events: %+v %v", observed, err)
	}
}
