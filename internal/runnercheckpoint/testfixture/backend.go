// Package testfixture provides deterministic checkpoint transport failure fixtures.
package testfixture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/artifactmanifest"
)

const PinnedReadFailure = "private-pinned-read-control"

type credential struct{}

func (credential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// PinnedReadBackend serves a real local fixture through Azure's native transport
// and fails the fourth source reservation read, inside ReusePinned.
type PinnedReadBackend struct {
	directory string
	reads     atomic.Int32
}

func (f *PinnedReadBackend) Reads() int32 { return f.reads.Load() }

func (f *PinnedReadBackend) Do(req *http.Request) (*http.Response, error) {
	key := strings.TrimPrefix(req.URL.Path, "/container/")
	if req.Method != http.MethodGet || key == "" || filepath.Base(key) != key {
		return nil, fmt.Errorf("unexpected fixture request: %s %s", req.Method, req.URL.Path)
	}
	if strings.HasPrefix(key, "execution-ledger-v1-reservation-") && f.reads.Add(1) == 4 {
		return nil, errors.New(PinnedReadFailure)
	}
	data, err := os.ReadFile(filepath.Join(f.directory, key))
	if err != nil {
		return nil, err
	}
	header := http.Header{}
	header.Set("Content-Length", strconv.Itoa(len(data)))
	header.Set("Etag", `"fixture"`)
	header.Set("X-Ms-Blob-Type", "BlockBlob")
	return &http.Response{StatusCode: http.StatusOK, Header: header,
		Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: req}, nil
}

func NewPinnedReadBackend(t *testing.T, directory string) (*artifactmanifest.Azure, *PinnedReadBackend) {
	t.Helper()
	transport := &PinnedReadBackend{directory: directory}
	backend, err := artifactmanifest.NewAzure("https://offline.invalid/container", credential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: transport, Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend, transport
}
