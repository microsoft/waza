package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/models"
)

type azureBlobTestTransport func(*http.Request) (*http.Response, error)

func (f azureBlobTestTransport) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAzureBlobServiceVersion(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		run        func(context.Context, *azblob.Client) error
	}{
		{
			name:       "upload",
			statusCode: http.StatusCreated,
			run: func(ctx context.Context, client *azblob.Client) error {
				_, err := client.UploadBuffer(ctx, "results", "run.json", []byte("{}"), nil)
				return err
			},
		},
		{
			name:       "list",
			statusCode: http.StatusOK,
			body:       `<EnumerationResults><Blobs/><NextMarker/></EnumerationResults>`,
			run: func(ctx context.Context, client *azblob.Client) error {
				_, err := client.NewListBlobsFlatPager("results", nil).NextPage(ctx)
				return err
			},
		},
		{
			name:       "download",
			statusCode: http.StatusOK,
			body:       "{}",
			run: func(ctx context.Context, client *azblob.Client) error {
				resp, err := client.DownloadStream(ctx, "results", "run.json", nil)
				if err != nil {
					return err
				}
				_, readErr := io.ReadAll(resp.Body)
				return errors.Join(readErr, resp.Body.Close())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				name := "retry succeeds"
				if fail {
					name = "retry exhausted"
				}
				t.Run(name, func(t *testing.T) {
					attempts := 0
					opts := azureBlobClientOptions()
					opts.Retry.MaxRetries = 1
					opts.Retry.RetryDelay = time.Nanosecond
					opts.Retry.MaxRetryDelay = time.Nanosecond
					opts.Transport = azureBlobTestTransport(func(req *http.Request) (*http.Response, error) {
						attempts++
						var versions []string
						for key, values := range req.Header {
							if strings.EqualFold(key, "x-ms-version") {
								versions = append(versions, values...)
							}
						}
						if len(versions) != 1 || versions[0] != "2026-10-06" {
							t.Errorf("attempt %d: x-ms-version values = %q, want exactly [2026-10-06]", attempts, versions)
						}
						status, body := tt.statusCode, tt.body
						header := make(http.Header)
						if attempts == 1 || fail {
							status = http.StatusServiceUnavailable
							body = `<Error><Code>ServerBusy</Code><Message>Retry later</Message></Error>`
							header.Set("x-ms-error-code", "ServerBusy")
						}
						// Ensure the policy reapplies the pin on every retry.
						req.Header.Set("x-ms-version", "2026-12-06")
						return &http.Response{
							StatusCode: status,
							Header:     header,
							Body:       io.NopCloser(strings.NewReader(body)),
							Request:    req,
						}, nil
					})
					client, err := azblob.NewClientWithNoCredential("https://example.blob.core.windows.net/", opts)
					if err != nil {
						t.Fatal(err)
					}
					err = tt.run(t.Context(), client)
					if fail {
						responseErr, ok := errors.AsType[*azcore.ResponseError](err)
						if !ok || responseErr.StatusCode != http.StatusServiceUnavailable {
							t.Fatalf("expected propagated 503 response error, got %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if attempts != 2 {
						t.Errorf("attempts = %d, want 2", attempts)
					}
				})
			}
		})
	}
}

// stubAzureBlobStore creates a stub store for testing methods that don't call Azure.
func stubAzureBlobStore() *AzureBlobStore {
	return &AzureBlobStore{client: nil, containerName: "test-container"}
}

// --- sanitizePathSegment ---

func TestSanitizePathSegment(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"has/slash", "has_slash"},
		{"back\\slash", "back_slash"},
		{"with:colon", "with_colon"},
		{"has space", "has_space"},
		{"a/b\\c:d e", "a_b_c_d_e"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := sanitizePathSegment(tt.in); got != tt.want {
			t.Errorf("sanitizePathSegment(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- stringPtr ---

func TestStringPtr(t *testing.T) {
	p := stringPtr("hello")
	if p == nil || *p != "hello" {
		t.Errorf("stringPtr(\"hello\") = %v, want pointer to \"hello\"", p)
	}
	p2 := stringPtr("")
	if p2 == nil || *p2 != "" {
		t.Error("stringPtr(\"\") should return pointer to empty string")
	}
}

// --- getMetadata ---

func TestGetMetadata(t *testing.T) {
	val := "myval"
	meta := map[string]*string{"key": &val}

	if got := getMetadata(meta, "key"); got != "myval" {
		t.Errorf("getMetadata present key = %q, want myval", got)
	}
	if got := getMetadata(meta, "missing"); got != "" {
		t.Errorf("getMetadata missing key = %q, want empty", got)
	}
	if got := getMetadata(nil, "key"); got != "" {
		t.Errorf("getMetadata nil map = %q, want empty", got)
	}
	meta["nilval"] = nil
	if got := getMetadata(meta, "nilval"); got != "" {
		t.Errorf("getMetadata nil value = %q, want empty", got)
	}
}

// --- isCI ---

func TestIsCI(t *testing.T) {
	// Save and clear all CI env vars.
	saved := make(map[string]string)
	for _, v := range ciEnvVars {
		saved[v] = os.Getenv(v)
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("Unsetenv(%s): %v", v, err)
		}
	}
	t.Cleanup(func() {
		for k, v := range saved {
			if v != "" {
				_ = os.Setenv(k, v)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})

	if isCI() {
		t.Error("isCI() = true when all CI vars unset")
	}

	// Setting any single var should return true.
	for _, v := range ciEnvVars {
		t.Setenv(v, "1")
		if !isCI() {
			t.Errorf("isCI() = false with %s set", v)
		}
		t.Setenv(v, "")
		if err := os.Unsetenv(v); err != nil {
			t.Fatalf("Unsetenv(%s): %v", v, err)
		}
	}
}

// --- blobToResultSummary ---

func TestBlobToResultSummary(t *testing.T) {
	abs := stubAzureBlobStore()
	ts := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	blob := &container.BlobItem{
		Name: stringPtr("skill-x/run-1.json"),
		Metadata: map[string]*string{
			"runid":     stringPtr("run-1"),
			"skill":     stringPtr("skill-x"),
			"model":     stringPtr("gpt-4o"),
			"passrate":  stringPtr("80.0"),
			"timestamp": stringPtr(ts.Format(time.RFC3339)),
		},
	}

	rs, err := abs.blobToResultSummary(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rs.RunID != "run-1" {
		t.Errorf("RunID = %q, want run-1", rs.RunID)
	}
	if rs.Skill != "skill-x" {
		t.Errorf("Skill = %q, want skill-x", rs.Skill)
	}
	if rs.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", rs.Model)
	}
	if rs.PassRate != 80.0 {
		t.Errorf("PassRate = %v, want 80.0", rs.PassRate)
	}
	if rs.BlobPath != "skill-x/run-1.json" {
		t.Errorf("BlobPath = %q, want skill-x/run-1.json", rs.BlobPath)
	}
}

func TestBlobToResultSummary_NilMetadata(t *testing.T) {
	abs := stubAzureBlobStore()
	blob := &container.BlobItem{Name: stringPtr("test.json"), Metadata: nil}

	_, err := abs.blobToResultSummary(blob)
	if err == nil {
		t.Error("expected error for nil metadata")
	}
}

func TestBlobToResultSummary_MissingRequiredFields(t *testing.T) {
	abs := stubAzureBlobStore()
	// Missing timestamp
	blob := &container.BlobItem{
		Name: stringPtr("test.json"),
		Metadata: map[string]*string{
			"runid": stringPtr("r1"),
		},
	}
	_, err := abs.blobToResultSummary(blob)
	if err == nil {
		t.Error("expected error for missing timestamp")
	}
}

func TestBlobToResultSummary_BadTimestamp(t *testing.T) {
	abs := stubAzureBlobStore()
	blob := &container.BlobItem{
		Name: stringPtr("test.json"),
		Metadata: map[string]*string{
			"runid":     stringPtr("r1"),
			"timestamp": stringPtr("not-a-time"),
		},
	}
	_, err := abs.blobToResultSummary(blob)
	if err == nil {
		t.Error("expected error for bad timestamp")
	}
}

func TestBlobToResultSummary_NoPassRate(t *testing.T) {
	abs := stubAzureBlobStore()
	ts := time.Now().Format(time.RFC3339)
	blob := &container.BlobItem{
		Name: stringPtr("test.json"),
		Metadata: map[string]*string{
			"runid":     stringPtr("r1"),
			"timestamp": stringPtr(ts),
		},
	}
	rs, err := abs.blobToResultSummary(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rs.PassRate != 0.0 {
		t.Errorf("PassRate = %v, want 0.0 for missing passrate", rs.PassRate)
	}
}

func TestBlobToResultSummary_NilBlobName(t *testing.T) {
	abs := stubAzureBlobStore()
	ts := time.Now().Format(time.RFC3339)
	blob := &container.BlobItem{
		Name: nil,
		Metadata: map[string]*string{
			"runid":     stringPtr("r1"),
			"timestamp": stringPtr(ts),
		},
	}
	rs, err := abs.blobToResultSummary(blob)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rs.BlobPath != "" {
		t.Errorf("BlobPath = %q, want empty for nil Name", rs.BlobPath)
	}
}

// --- AzureBlobStore.outcomeToResultSummary ---

func TestAzureBlobStore_OutcomeToResultSummary(t *testing.T) {
	abs := stubAzureBlobStore()
	o := &models.EvaluationOutcome{
		RunID:       "run-42",
		SkillTested: "my/skill",
		Timestamp:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		Setup:       models.OutcomeSetup{ModelID: "gpt-4o"},
		Digest:      models.OutcomeDigest{TotalTests: 10, Succeeded: 7},
	}

	rs := abs.outcomeToResultSummary(o)
	if rs.RunID != "run-42" {
		t.Errorf("RunID = %q", rs.RunID)
	}
	if rs.PassRate != 70.0 {
		t.Errorf("PassRate = %v, want 70.0", rs.PassRate)
	}
	// Skill contains a slash which gets sanitized in the blob path.
	want := "my_skill/run-42.json"
	if rs.BlobPath != want {
		t.Errorf("BlobPath = %q, want %q", rs.BlobPath, want)
	}
}

func TestAzureBlobStore_OutcomeToResultSummary_ZeroTests(t *testing.T) {
	abs := stubAzureBlobStore()
	o := &models.EvaluationOutcome{
		RunID:       "empty-run",
		SkillTested: "s",
		Digest:      models.OutcomeDigest{TotalTests: 0, Succeeded: 0},
	}
	rs := abs.outcomeToResultSummary(o)
	if rs.PassRate != 0.0 {
		t.Errorf("PassRate = %v, want 0.0 for zero tests", rs.PassRate)
	}
}

// --- NewAzureBlobStore validation ---

func TestNewAzureBlobStore_MissingAccountName(t *testing.T) {
	_, err := NewAzureBlobStore(t.Context(), "", "container")
	if err == nil {
		t.Error("expected error for empty account name")
	}
}

func TestNewAzureBlobStore_MissingContainerName(t *testing.T) {
	_, err := NewAzureBlobStore(t.Context(), "account", "")
	if err == nil {
		t.Error("expected error for empty container name")
	}
}
