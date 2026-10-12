package webapi

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"runtime"

	"github.com/microsoft/waza/internal/releasepolicy"
)

// ReleaseUsage transports exact numeric tokens as strings so JavaScript does
// not round large integer token counts before rendering them.
type ReleaseUsage struct {
	releasepolicy.UsageAxis
	Value *string `json:"value,omitempty"`
}

type ReleaseDecision struct {
	releasepolicy.Decision
	Kind  string                               `json:"kind"`
	Usage map[releasepolicy.Arm][]ReleaseUsage `json:"usage"`
}

type ReleaseCollection struct {
	Path     string          `json:"path"`
	Decision ReleaseDecision `json:"decision"`
	Error    string          `json:"error,omitempty"`
}

func releaseView(d releasepolicy.Decision) ReleaseDecision {
	view := ReleaseDecision{Decision: d, Kind: "waza.release-decision-view",
		Usage: map[releasepolicy.Arm][]ReleaseUsage{}}
	for arm, axes := range d.Usage {
		for _, axis := range axes {
			item := ReleaseUsage{UsageAxis: axis}
			if axis.Value != nil {
				value := axis.Value.String()
				item.Value = &value
			}
			view.Usage[arm] = append(view.Usage[arm], item)
		}
	}
	return view
}

// RegisterReleaseRoutes uses only the configured local results root; the
// dashboard never supplies an arbitrary filesystem path or assesses summaries
// as ordinary runs. Each response re-verifies the collection artifacts.
func RegisterReleaseRoutes(mux *http.ServeMux, directory string) {
	registerReleaseRoutes(mux, directory, os.OpenRoot, (*os.Root).Close)
}

func registerReleaseRoutes(mux *http.ServeMux, directory string,
	openRoot func(string) (*os.Root, error), closeRoot func(*os.Root) error) {
	mux.HandleFunc("GET /api/release-collections", func(w http.ResponseWriter, request *http.Request) {
		if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("retained release collection confinement is unsupported on %s", runtime.GOOS))
			return
		}
		if err := request.Context().Err(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		root, err := openRoot(directory)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("opening local release collections: %v", err))
			return
		}
		collections, readErr := readReleaseCollections(request.Context(), root, closeRoot)
		closeErr := closeRoot(root)
		err = errors.Join(readErr, closeErr, request.Context().Err())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, collections)
	})
}

func readReleaseCollections(ctx context.Context, root *os.Root, closeRoot func(*os.Root) error) ([]ReleaseCollection, error) {
	collections := []ReleaseCollection{}
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "policy.json" {
			return nil
		}
		relative := path.Dir(name)
		collection, err := root.OpenRoot(relative)
		if err != nil {
			return err
		}
		item, inspectErr := readReleaseCollection(ctx, collection, relative)
		closeErr := closeRoot(collection)
		if err := errors.Join(inspectErr, closeErr, ctx.Err()); err != nil {
			return err
		}
		collections = append(collections, item)
		return nil
	})
	return collections, err
}

func readReleaseCollection(ctx context.Context, collection *os.Root, relative string) (ReleaseCollection, error) {
	// Reject symlinked artifacts rather than allowing fixed filenames
	// inside a collection to escape the configured root.
	for _, name := range []string{"policy.json", "journal.json", "journal.ndjson",
		"baseline.begin.json", "candidate.begin.json", "baseline.final.json", "candidate.final.json",
		"baseline.results.json", "candidate.results.json", "baseline.result-binding.json", "candidate.result-binding.json"} {
		info, err := collection.Lstat(name)
		if os.IsNotExist(err) {
			continue // Missing publication is an explicit nonpass state.
		}
		if err != nil {
			return ReleaseCollection{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ReleaseCollection{}, fmt.Errorf("release collection artifact is a symlink: %s", name)
		}
	}
	decision, readErr := releasepolicy.ReadDecisionRootContext(ctx, collection)
	item := ReleaseCollection{Path: relative, Decision: releaseView(decision)}
	if readErr != nil {
		item.Decision.Accepted = false
		item.Error = readErr.Error()
	}
	return item, nil
}
