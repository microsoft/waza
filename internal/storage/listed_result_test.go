package storage

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

const listedBlobXML = `<EnumerationResults><Blobs><Blob><Name>skill/run.json</Name><Properties/><Metadata><runid>run</runid><skill>skill</skill><timestamp>2026-10-05T12:00:00Z</timestamp></Metadata></Blob></Blobs><NextMarker/></EnumerationResults>`

type failingBlobBody struct {
	io.Reader
	readErr  error
	closeErr error
}

func (b failingBlobBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.Reader.Read(p)
}

func (b failingBlobBody) Close() error {
	return b.closeErr
}

func TestAzureListedResultDownloadsKnownPath(t *testing.T) {
	for _, mode := range []string{"listed", "known path", "missing path"} {
		t.Run(mode, func(t *testing.T) {
			lists, downloads := 0, 0
			data, err := json.Marshal(&models.EvaluationOutcome{RunID: "run"})
			require.NoError(t, err)
			opts := azureBlobClientOptions()
			opts.Transport = azureBlobTestTransport(func(req *http.Request) (*http.Response, error) {
				body := string(data)
				if req.URL.Query().Get("comp") == "list" {
					lists++
					body = listedBlobXML
					if mode == "listed" && req.URL.Query().Get("include") != "metadata" {
						t.Errorf("list did not request metadata: %s", req.URL)
					}
				} else {
					downloads++
					if req.URL.Path != "/results/skill/run.json" {
						t.Errorf("wrong download path %q", req.URL.Path)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			client, err := azblob.NewClientWithNoCredential("https://example.blob.core.windows.net/", opts)
			require.NoError(t, err)
			store := &AzureBlobStore{client: client, containerName: "results"}
			result := ResultSummary{RunID: "run", BlobPath: "skill/run.json"}
			switch mode {
			case "listed":
				results, err := store.List(t.Context(), ListOptions{})
				require.NoError(t, err)
				require.Len(t, results, 1)
				result = results[0]
			case "missing path":
				result.BlobPath = ""
			}
			repeats := 10
			if mode == "missing path" {
				repeats = 1
			}
			for range repeats {
				outcome, err := DownloadListedResult(t.Context(), store, result)
				require.NoError(t, err)
				require.Equal(t, "run", outcome.RunID)
			}
			require.Equal(t, repeats, downloads)
			wantLists := 0
			if mode != "known path" {
				wantLists = 1
			}
			require.Equal(t, wantLists, lists)
		})
	}
}

func TestAzureListedResultDownloadErrors(t *testing.T) {
	for _, failure := range []string{"request", "read", "parse", "close"} {
		t.Run(failure, func(t *testing.T) {
			opts := azureBlobClientOptions()
			opts.Retry.MaxRetries = -1
			opts.Transport = azureBlobTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("comp") == "list" {
					t.Error("known path unexpectedly enumerated blobs")
				}
				if failure == "request" {
					return nil, errors.New("request failed")
				}
				body := failingBlobBody{Reader: strings.NewReader(`{"run_id":"run"}`)}
				if failure == "read" {
					body.readErr = errors.New("read failed")
				}
				if failure == "parse" {
					body.Reader = strings.NewReader("invalid JSON")
				}
				if failure == "close" {
					body.closeErr = errors.New("close failed")
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
			})
			client, err := azblob.NewClientWithNoCredential("https://example.blob.core.windows.net/", opts)
			require.NoError(t, err)
			store := &AzureBlobStore{client: client, containerName: "results"}
			_, err = DownloadListedResult(t.Context(), store, ResultSummary{RunID: "run", BlobPath: "skill/run.json"})
			require.Error(t, err)
			if failure == "parse" {
				require.ErrorContains(t, err, "unmarshaling outcome")
			} else {
				require.ErrorContains(t, err, failure+" failed")
			}
		})
	}
}

func TestDownloadListedResultSupportsLegacyStores(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	require.NoError(t, store.Upload(t.Context(), &models.EvaluationOutcome{RunID: "run"}))
	outcome, err := DownloadListedResult(t.Context(), store, ResultSummary{RunID: "run"})
	require.NoError(t, err)
	require.Equal(t, "run", outcome.RunID)
	_, err = DownloadListedResult(t.Context(), store, ResultSummary{RunID: "missing"})
	require.ErrorIs(t, err, ErrNotFound)
}
