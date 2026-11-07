package artifactmanifest

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

// OpenBackend opens the explicit Azure container when supplied, otherwise the
// existing local directory. It never signs in, creates directories, or falls
// back to local storage after an Azure configuration or credential error.
func OpenBackend(directory, azureURL string) (Backend, func() error, error) {
	if azureURL == "" {
		backend, err := OpenLocal(directory)
		if err != nil {
			return nil, nil, err
		}
		return backend, backend.Close, nil
	}
	if err := validateAzureURL(azureURL); err != nil {
		return nil, nil, err
	}
	credential, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("creating explicit Azure credential chain: %w", err)
	}
	backend, err := NewAzure(azureURL, credential, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{PerRetryPolicies: []policy.Policy{VersionPolicy{}}},
	})
	if err != nil {
		return nil, nil, err
	}
	return backend, func() error { return nil }, nil
}
