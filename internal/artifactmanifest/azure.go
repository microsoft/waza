package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
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
type Azure struct {
	client       *container.Client
	retryOptions policy.RetryOptions
}

func NewAzure(containerURL string, credential azcore.TokenCredential, options *container.ClientOptions) (*Azure, error) {
	if err := validateAzureURL(containerURL); err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, errors.New("immutable Azure store requires an explicit credential")
	}
	clientOptions := container.ClientOptions{}
	if options != nil {
		clientOptions = *options
		clientOptions.PerCallPolicies = slices.Clone(options.PerCallPolicies)
		clientOptions.PerRetryPolicies = slices.Clone(options.PerRetryPolicies)
	}
	clientOptions.PerRetryPolicies = slices.DeleteFunc(clientOptions.PerRetryPolicies, func(p policy.Policy) bool {
		switch p.(type) {
		case VersionPolicy, *VersionPolicy:
			return true
		default:
			return false
		}
	})
	clientOptions.PerRetryPolicies = append(clientOptions.PerRetryPolicies, VersionPolicy{})
	client, err := container.NewClient(containerURL, credential, &clientOptions)
	if err != nil {
		return nil, fmt.Errorf("creating immutable Azure client: %w", err)
	}
	return &Azure{client: client, retryOptions: clientOptions.Retry}, nil
}

func validateAzureURL(containerURL string) error {
	u, err := url.Parse(containerURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("immutable Azure store requires an HTTPS container URL without credentials or query")
	}
	return nil
}

func (a *Azure) ListManifests(ctx context.Context) ([]string, bool, error) {
	return a.listObjects(ctx, "manifest-")
}

func (a *Azure) listObjects(ctx context.Context, prefix string) ([]string, bool, error) {
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

// listAllObjects follows continuation pages and returns at most limit keys;
// truncated reports that more keys exist.
func (a *Azure) listAllObjects(ctx context.Context, prefix string, limit int) ([]string, bool, error) {
	size := int32(MaxCatalogEntries)
	pager := a.client.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix, MaxResults: &size})
	keys := []string{}
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, false, err
		}
		if page.Segment == nil || len(page.Segment.BlobItems) > MaxCatalogEntries {
			return nil, false, ErrCorrupt
		}
		for _, item := range page.Segment.BlobItems {
			if item == nil || item.Name == nil || !strings.HasPrefix(*item.Name, prefix) {
				return nil, false, ErrCorrupt
			}
			if len(keys) == limit {
				return keys, true, nil
			}
			keys = append(keys, *item.Name)
		}
	}
	return keys, false, nil
}

func (a *Azure) Create(ctx context.Context, key string, data []byte) (bool, error) {
	if !validKey(key) || len(data) > MaxArtifactBytes {
		return false, ErrInvalid
	}
	if err := verifyObject(key, data); err != nil {
		return false, err
	}
	return a.createObject(ctx, key, data)
}

func (a *Azure) createObject(ctx context.Context, key string, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	none := azcore.ETag("*")
	_, err := a.client.NewBlockBlobClient(key).Upload(ctx, streaming.NopCloser(bytes.NewReader(data)), &blockblob.UploadOptions{
		AccessConditions: &blob.AccessConditions{ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: &none}},
	})
	if err != nil {
		var response *azcore.ResponseError
		if errors.As(err, &response) {
			occupied := response.StatusCode == http.StatusPreconditionFailed && response.ErrorCode == "ConditionNotMet" ||
				response.StatusCode == http.StatusConflict && response.ErrorCode == "BlobAlreadyExists"
			if occupied {
				return false, nil
			}
		}
		return false, fmt.Errorf("conditional Azure object upload: %w", err)
	}
	return true, nil
}

func (a *Azure) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	if !validKey(key) {
		return nil, ErrInvalid
	}
	data, err := a.readObject(ctx, key, limit)
	if err != nil {
		return nil, err
	}
	if err := verifyObject(key, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (a *Azure) readObject(ctx context.Context, key string, limit int64) ([]byte, error) {
	if limit < 0 || limit > MaxArtifactBytes {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
