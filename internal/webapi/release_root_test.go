package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/stretchr/testify/require"
)

func TestReleaseRouteRetainsConfiguredRootAcrossReplacement(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, "original"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "original", "policy.json"), []byte(`{"original":true}`), 0o600))
	outside := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(outside, "replacement"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "replacement", "policy.json"), []byte(`{"outside":true}`), 0o600))
	moved := directory + "-retained"
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(moved)) })
	var opened []*os.Root
	mux := http.NewServeMux()
	registerReleaseRoutes(mux, directory, func(name string) (*os.Root, error) {
		root, err := os.OpenRoot(name)
		if err != nil {
			return nil, err
		}
		opened = append(opened, root)
		require.NoError(t, os.Rename(directory, moved))
		if runtime.GOOS == "windows" {
			require.NoError(t, os.Mkdir(directory, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), []byte(`{"replacement":true}`), 0o600))
		} else {
			require.NoError(t, os.Symlink(outside, directory))
		}
		return root, nil
	}, func(root *os.Root) error {
		opened = append(opened, root)
		return root.Close()
	})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/release-collections", nil))
	require.Equal(t, http.StatusOK, response.Code, "%s", response.Body.String())
	var collections []ReleaseCollection
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &collections))
	require.Len(t, collections, 1)
	require.Equal(t, "original", collections[0].Path, "actual route must not discover the replacement root")
	require.False(t, collections[0].Decision.Accepted)
	require.Equal(t, "invalid", collections[0].Decision.Compatibility.State)
	require.NotEmpty(t, collections[0].Error)
	for _, root := range opened {
		_, err := root.Stat(".")
		require.ErrorIs(t, err, os.ErrClosed, "all owned handles close before HTTP output")
	}
}

func TestReleaseRouteCloseAndCancellationPreventPublication(t *testing.T) {
	for _, mode := range []string{"request_close", "collection_close", "cancel_before_open", "cancel_during_close", "closed_root"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "policy.json"), []byte(`{}`), 0o600))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancel_before_open" {
				cancel()
			}
			failure := errors.New("synthetic HTTP root close failure")
			calls, closes := 0, 0
			var requestRoot *os.Root
			mux := http.NewServeMux()
			registerReleaseRoutes(mux, directory, func(name string) (*os.Root, error) {
				calls++
				root, err := os.OpenRoot(name)
				requestRoot = root
				if err == nil && mode == "closed_root" {
					require.NoError(t, root.Close())
				}
				return root, err
			}, func(root *os.Root) error {
				closes++
				err := root.Close()
				if mode == "cancel_during_close" && root == requestRoot {
					cancel()
				}
				if mode == "request_close" && root == requestRoot ||
					mode == "collection_close" && root != requestRoot {
					return errors.Join(err, failure)
				}
				return err
			})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/release-collections", nil).WithContext(ctx)
			mux.ServeHTTP(response, request)
			require.Equal(t, http.StatusInternalServerError, response.Code, "%s", response.Body.String())
			require.NotContains(t, response.Body.String(), `"accepted":true`)
			if mode == "cancel_before_open" {
				require.Zero(t, calls)
				require.Zero(t, closes)
			} else {
				require.Equal(t, 1, calls)
				require.Positive(t, closes)
				_, err := requestRoot.Stat(".")
				require.ErrorIs(t, err, os.ErrClosed)
			}
			if mode == "request_close" || mode == "collection_close" {
				require.Contains(t, response.Body.String(), failure.Error())
			}
		})
	}
}

func TestReleaseRouteRejectsFixedNonRegularArtifact(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, "policy.json"), 0o700))
	root, err := os.OpenRoot(directory)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	item, err := readReleaseCollection(context.Background(), root, ".")
	require.NoError(t, err)
	require.False(t, item.Decision.Accepted)
	require.Contains(t, item.Error, "regular file")
	require.Equal(t, releasepolicy.InitialDecision().Version, item.Decision.Version)
}
