package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

// Azure uses single-object conditional PUT, not the legacy overwrite upload.
// The caller supplies credentials explicitly; construction never signs in.
type Azure struct{ client *container.Client }

func NewAzure(containerURL string, credential azcore.TokenCredential, options *container.ClientOptions) (*Azure, error) {
	u, err := url.Parse(containerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("immutable Azure store requires an HTTPS container URL without credentials or query")
	}
	if credential == nil {
		return nil, errors.New("immutable Azure store requires an explicit credential")
	}
	client, err := container.NewClient(containerURL, credential, options)
	if err != nil {
		return nil, fmt.Errorf("creating immutable Azure client: %w", err)
	}
	return &Azure{client: client}, nil
}

func (a *Azure) ListManifests(ctx context.Context) ([]string, bool, error) {
	prefix := "manifest-"
	size := int32(MaxCatalogEntries)
	pager := a.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix, MaxResults: &size})
	if !pager.More() {
		return []string{}, false, nil
	}
	page, err := pager.NextPage(ctx)
	if err != nil {
		return nil, false, err
	}
	keys := []string{}
	if page.Segment == nil {
		return nil, false, ErrCorrupt
	}
	for _, item := range page.Segment.BlobItems {
		if item == nil || item.Name == nil {
			return nil, false, ErrCorrupt
		}
		keys = append(keys, *item.Name)
	}
	if len(keys) > MaxCatalogEntries {
		return nil, false, ErrCorrupt
	}
	return keys, pager.More(), nil
}

func (a *Azure) Create(ctx context.Context, key string, data []byte) (bool, error) {
	if !validKey(key) || len(data) > MaxArtifactBytes {
		return false, ErrInvalid
	}
	if err := verifyObject(key, data); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	none := azcore.ETag("*")
	_, err := a.client.NewBlockBlobClient(key).Upload(ctx, streaming.NopCloser(bytes.NewReader(data)), &blockblob.UploadOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: &none}},
	})
	if err != nil {
		var response *azcore.ResponseError
		if errors.As(err, &response) && response.StatusCode == http.StatusPreconditionFailed && response.ErrorCode == "ConditionNotMet" {
			return false, nil
		}
		return false, fmt.Errorf("conditional Azure object upload: %w", err)
	}
	return true, nil
}

func (a *Azure) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if !validKey(key) || limit < 0 || limit > MaxArtifactBytes {
		return nil, ErrInvalid
	}
	response, err := a.client.NewBlobClient(key).DownloadStream(ctx, nil)
	if err != nil {
		var failure *azcore.ResponseError
		if errors.As(err, &failure) && failure.StatusCode == http.StatusNotFound && failure.ErrorCode == "BlobNotFound" {
			return nil, ErrMissing
		}
		return nil, fmt.Errorf("downloading immutable Azure object: %w", err)
	}
	if response.Body == nil {
		return nil, ErrCorrupt
	}
	if response.ContentLength == nil || *response.ContentLength < 0 || *response.ContentLength > limit {
		return nil, errors.Join(ErrCorrupt, response.Body.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if int64(len(data)) > limit || int64(len(data)) != *response.ContentLength {
		readErr = errors.Join(readErr, ErrCorrupt)
	}
	if err := errors.Join(readErr, response.Body.Close(), ctx.Err()); err != nil {
		return nil, err
	}
	if err := verifyObject(key, data); err != nil {
		return nil, err
	}
	return data, nil
}

// VersionPolicy follows the pinned service version used by legacy storage,
// without importing its ambient-credential and automatic-login constructors.
type VersionPolicy struct{}

func (VersionPolicy) Do(req *policy.Request) (*http.Response, error) {
	delete(req.Raw().Header, strings.ToLower("X-Ms-Version"))
	req.Raw().Header.Set("X-Ms-Version", "2026-10-06")
	return req.Next()
}
