package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func publicationFixture(t *testing.T) (Manifest, []byte) {
	t.Helper()
	wire, err := os.ReadFile("testdata/publication-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(wire)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/publication-terminal.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyBytes(data, m.Artifacts[0].SHA256, m.Artifacts[0].SizeBytes); err != nil {
		t.Fatal(err)
	}
	if _, err := runnerterminal.Validate(data, m.Binding.TerminalBinding()); err != nil {
		t.Fatal(err)
	}
	return *m, data
}

type fakeAzure struct {
	mu               sync.Mutex
	objects          map[string][]byte
	failPut          error
	failCommittedPut error
}

func (f *fakeAzure) Do(r *http.Request) (*http.Response, error) {
	key := strings.TrimPrefix(r.URL.Path, "/container/")
	var data []byte
	if r.Body != nil {
		var err error
		data, err = io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	header := http.Header{"X-Ms-Request-Id": {"offline"}, "Etag": {`"test"`}, "Last-Modified": {time.Now().UTC().Format(http.TimeFormat)}}
	response := func(status int, body []byte) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	}
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("If-None-Match") != "*" {
			return nil, errors.New("upload omitted conditional no-replace fence")
		}
		if f.failPut != nil {
			return nil, f.failPut
		}
		if _, exists := f.objects[key]; exists {
			header.Set("X-Ms-Error-Code", "ConditionNotMet")
			return response(http.StatusPreconditionFailed, []byte("<Error><Code>ConditionNotMet</Code></Error>"))
		}
		f.objects[key] = append([]byte(nil), data...)
		if (strings.HasPrefix(key, "final-") || strings.HasPrefix(key, namespacePrefix)) && f.failCommittedPut != nil {
			err := f.failCommittedPut
			f.failCommittedPut = nil
			return nil, err
		}
		return response(http.StatusCreated, nil)
	case http.MethodGet:
		if r.URL.Query().Get("comp") == "list" {
			prefix := r.URL.Query().Get("prefix")
			if prefix == "" {
				return nil, errors.New("discovery omitted its namespace prefix")
			}
			var listing strings.Builder
			listing.WriteString("<EnumerationResults><Blobs>")
			names := []string{}
			for name := range f.objects {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
				}
			}
			slices.Sort(names)
			marker := ""
			if len(names) > MaxCatalogEntries {
				names = names[:MaxCatalogEntries]
				marker = "remaining"
			}
			for _, name := range names {
				listing.WriteString("<Blob><Name>" + name + "</Name><Properties><Content-Length>1</Content-Length><BlobType>BlockBlob</BlobType></Properties></Blob>")
			}
			listing.WriteString("</Blobs><NextMarker>" + marker + "</NextMarker></EnumerationResults>")
			return response(http.StatusOK, []byte(listing.String()))
		}
		data, exists := f.objects[key]
		if !exists {
			header.Set("X-Ms-Error-Code", "BlobNotFound")
			return response(http.StatusNotFound, []byte("<Error><Code>BlobNotFound</Code></Error>"))
		}
		header.Set("Content-Length", strconv.Itoa(len(data)))
		header.Set("X-Ms-Blob-Type", "BlockBlob")
		return response(http.StatusOK, data)
	default:
		return nil, errors.New("unexpected Azure operation")
	}
}

type offlineCredential struct{}

func (offlineCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline-not-a-secret", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func azureFixture(t *testing.T) (*Azure, *fakeAzure) {
	t.Helper()
	transport := &fakeAzure{objects: make(map[string][]byte)}
	return azureBackend(t, transport), transport
}

func azureBackend(t *testing.T, transport policy.Transporter) *Azure {
	t.Helper()
	backend, err := NewAzure("https://offline.invalid/container", offlineCredential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: transport, Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func TestPublicationBackendContracts(t *testing.T) {
	for _, provider := range []string{"local", "azure-offline-transport"} {
		t.Run(provider, func(t *testing.T) {
			var backend Backend
			var tamper func(string, []byte)
			if provider == "local" {
				directory := t.TempDir()
				local, err := OpenLocal(directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := local.Close(); err != nil {
						t.Error(err)
					}
				})
				backend = local
				tamper = func(key string, data []byte) {
					t.Helper()
					if err := os.Chmod(filepath.Join(directory, key), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(directory, key), data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				azure, transport := azureFixture(t)
				backend = azure
				tamper = func(key string, data []byte) {
					transport.mu.Lock()
					defer transport.mu.Unlock()
					transport.objects[key] = append([]byte(nil), data...)
				}
			}
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
			again, err := store.Create(context.Background(), wire)
			if err != nil || digest != again {
				t.Fatalf("create not idempotent: %s %s %v", digest, again, err)
			}
			r, err := store.Finalize(context.Background(), digest)
			if !errors.Is(err, ErrMissing) || r.Integrity != "failed" || r.Publication == "finalized" {
				t.Fatalf("partial upload finalized: %+v %v", r, err)
			}
			a := m.Artifacts[0]
			if err := store.Put(context.Background(), a, data[:len(data)-1]); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("partial content accepted: %v", err)
			}
			if err := store.Put(context.Background(), a, data); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			errs := make(chan error, 24)
			start := make(chan struct{})
			for range 24 {
				wg.Go(func() {
					<-start
					if err := store.Put(context.Background(), a, data); err != nil {
						errs <- err
						return
					}
					r, err := store.Finalize(context.Background(), digest)
					if err == nil && (r.Publication != "finalized" || r.Integrity != "verified") {
						err = errors.New("missing verified finalized receipt")
					}
					errs <- err
				})
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			// Distinct immutable manifests may share a declared logical identity.
			alternate := m
			alternate.Artifacts = append(append([]Artifact(nil), m.Artifacts...), Artifact{
				Name: "empty.txt", SHA256: Hash(nil), SizeBytes: 0, MediaType: "text/plain",
			})
			otherWire, err := Encode(alternate)
			if err != nil {
				t.Fatal(err)
			}
			otherDigest, err := store.Create(context.Background(), otherWire)
			if err != nil || otherDigest == digest {
				t.Fatalf("alternate manifest setup: %s %v", otherDigest, err)
			}
			if err := store.Put(context.Background(), alternate.Artifacts[1], nil); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Finalize(context.Background(), otherDigest); err != nil {
				t.Fatalf("logical identity wrongly prevented distinct publication: %v", err)
			}
			catalog, err := store.Catalog(context.Background())
			if err != nil || len(catalog.ManifestSHA256) != 2 || catalog.Truncated || catalog.Verification != "unverified-pointers-only" {
				t.Fatalf("persisted catalog pointers unavailable: %+v %v", catalog, err)
			}
			tamper("final-"+digest, []byte(strings.Repeat("b", 64)))
			r, err = store.Finalize(context.Background(), digest)
			if !errors.Is(err, ErrConflict) || r.Publication != "conflict" || r.Integrity == "verified" {
				t.Fatalf("conflicting finalization silently replaced: %+v %v", r, err)
			}
			tamper("final-"+digest, []byte(digest))
			corrupt := append([]byte(nil), data...)
			corrupt[0] ^= 1
			tamper("blob-"+a.SHA256, corrupt)
			r, err = store.Inspect(context.Background(), digest)
			if !errors.Is(err, ErrCorrupt) || r.Integrity != "failed" || r.Artifacts[0].Integrity != "corrupt" {
				t.Fatalf("corruption became verified: %+v %v", r, err)
			}
			if err := store.Put(context.Background(), a, data); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corrupt stored blob silently overwritten: %v", err)
			}
		})
	}
}

type failingWriter struct {
	*os.File
	writeErr, closeErr, chmodErr error
	short                        bool
}

func (w failingWriter) Write(data []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return w.File.Write(data[:len(data)/2])
	}
	return w.File.Write(data)
}

func (w failingWriter) Close() error { return errors.Join(w.File.Close(), w.closeErr) }

func (w failingWriter) Chmod(mode os.FileMode) error {
	if w.chmodErr != nil {
		return w.chmodErr
	}
	return w.File.Chmod(mode)
}

func TestLocalProductionStagingFailuresAndReplacement(t *testing.T) {
	for _, mode := range []string{"write", "short", "chmod", "close", "cancel", "link", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			local, err := OpenLocal(directory)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := local.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var staging string
			fault := errors.New("injected " + mode)
			local.openStage = func(name string) (stageFile, error) {
				staging = name
				file, err := local.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					return nil, err
				}
				w := failingWriter{File: file}
				switch mode {
				case "write":
					w.writeErr = fault
				case "short":
					w.short = true
				case "close":
					w.closeErr = fault
				case "chmod":
					w.chmodErr = fault
				case "cancel":
					cancel()
				}
				return w, nil
			}
			local.beforeLink = func(name, key string) error {
				if mode == "link" {
					return fault
				}
				if mode == "replacement" {
					if err := local.root.Rename(name, name+"-owned"); err != nil {
						return err
					}
					return local.root.WriteFile(name, []byte("competitor"), 0o600)
				}
				return nil
			}
			data := []byte("original content")
			a := Artifact{SHA256: Hash(data), SizeBytes: int64(len(data))}
			err = NewStore(local).Put(ctx, a, data)
			if err == nil {
				t.Fatal("injected staging failure returned success")
			}
			if mode == "replacement" {
				if !errors.Is(err, ErrCorrupt) {
					t.Fatalf("replacement not classified as corruption: %v", err)
				}
				got, err := local.root.ReadFile(staging)
				if err != nil || string(got) != "competitor" {
					t.Fatalf("competing staging entry removed: %q %v", got, err)
				}
				got, err = local.root.ReadFile(staging + "-owned")
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("displaced owned bytes lost: %q %v", got, err)
				}
			} else if _, err := local.root.Lstat("blob-" + a.SHA256); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed staging exposed final object: %v", err)
			}
			if _, err := local.root.Lstat(staging); err != nil {
				t.Fatalf("staging residue was unsafely removed: %v", err)
			}
		})
	}
}
