package artifactmanifest

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
)

type callerVersionPolicy struct{}

func (callerVersionPolicy) Do(request *policy.Request) (*http.Response, error) {
	request.Raw().Header.Set("X-Caller-Policy", "preserved")
	request.Raw().Header.Set("X-Ms-Version", "2099-01-01")
	return request.Next()
}

type versionTransport struct {
	t     *testing.T
	inner *fakeAzure
	calls int
}

func (v *versionTransport) Do(request *http.Request) (*http.Response, error) {
	v.calls++
	var versions []string
	for key, values := range request.Header {
		if strings.EqualFold(key, "x-ms-version") {
			versions = append(versions, values...)
		}
	}
	if !reflect.DeepEqual(versions, []string{"2026-10-06"}) {
		v.t.Errorf("outgoing SDK service versions: %q", versions)
	}
	if request.Header.Get("X-Caller-Policy") != "preserved" {
		v.t.Error("caller policy was discarded")
	}
	return v.inner.Do(request)
}

func TestNewAzurePinsVersionWithoutMutatingCallerPolicies(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		policies []policy.Policy
	}{
		{"absent", []policy.Policy{callerVersionPolicy{}}},
		{"value", []policy.Policy{VersionPolicy{}, callerVersionPolicy{}}},
		{"pointer-and-value", []policy.Policy{&VersionPolicy{}, callerVersionPolicy{}, VersionPolicy{}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			supplied := scenario.policies
			backing := make([]policy.Policy, len(supplied)+4)
			copy(backing, supplied)
			backing[len(supplied)] = callerVersionPolicy{}
			before := slices.Clone(backing)
			perCall := []policy.Policy{callerVersionPolicy{}, VersionPolicy{}}
			beforeCall := slices.Clone(perCall)
			transport := &versionTransport{t: t, inner: &fakeAzure{objects: map[string][]byte{}}}
			options := &container.ClientOptions{ClientOptions: azcore.ClientOptions{
				Transport: transport, Retry: policy.RetryOptions{MaxRetries: -1},
				PerCallPolicies:  perCall[:1],
				PerRetryPolicies: backing[:len(supplied)],
			}}
			backend, err := NewAzure("https://offline.invalid/container", offlineCredential{}, options)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("original artifact")
			key := "blob-" + Hash(data)
			if created, err := backend.Create(context.Background(), key, data); err != nil || !created {
				t.Fatalf("direct conditional create: %v %v", created, err)
			}
			if _, err := backend.Get(context.Background(), key, int64(len(data))); err != nil {
				t.Fatal(err)
			}
			if _, _, err := backend.ListManifests(context.Background()); err != nil {
				t.Fatal(err)
			}
			if transport.calls != 3 {
				t.Fatalf("SDK upload/download/list controls executed %d requests", transport.calls)
			}
			if !reflect.DeepEqual(backing, before) || !reflect.DeepEqual(options.PerRetryPolicies, supplied) ||
				!reflect.DeepEqual(perCall, beforeCall) || len(options.PerCallPolicies) != 1 {
				t.Fatal("construction or requests mutated caller policy slices")
			}
		})
	}
	if backend, err := NewAzure("https://offline.invalid/container", offlineCredential{}, nil); err != nil || backend == nil {
		t.Fatalf("nil-options construction: %v", err)
	}
}
