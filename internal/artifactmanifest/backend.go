package artifactmanifest

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

// OpenBackend opens the explicit Azure container when supplied, otherwise the
// existing local directory. It never initiates interactive login, creates
// directories, or falls back after an Azure error. Token acquisition may invoke
// installed Azure credential helpers; provider authentication is not process-free.
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
