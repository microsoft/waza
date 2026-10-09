package webapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	mux.HandleFunc("GET /api/release-collections", func(w http.ResponseWriter, _ *http.Request) {
		root, err := filepath.EvalSymlinks(directory)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("opening local release collections: %v", err))
			return
		}
		root, err = filepath.Abs(root)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		collections := []ReleaseCollection{}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "policy.json" {
				return nil
			}
			parent := filepath.Dir(path)
			// Reject symlinked artifacts rather than allowing fixed filenames
			// inside a collection to escape the configured root.
			for _, name := range []string{"policy.json", "journal.json", "journal.ndjson",
				"baseline.begin.json", "candidate.begin.json", "baseline.final.json", "candidate.final.json",
				"baseline.results.json", "candidate.results.json", "baseline.result-binding.json", "candidate.result-binding.json"} {
				info, err := os.Lstat(filepath.Join(parent, name))
				if os.IsNotExist(err) {
					continue // Missing publication is an explicit nonpass state.
				}
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("release collection artifact is a symlink: %s", name)
				}
			}
			relative, err := filepath.Rel(root, parent)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return fmt.Errorf("release collection escapes results root")
			}
			decision, readErr := releasepolicy.ReadDecision(parent)
			item := ReleaseCollection{Path: filepath.ToSlash(relative), Decision: releaseView(decision)}
			if readErr != nil {
				item.Decision.Accepted = false
				item.Error = readErr.Error()
			}
			collections = append(collections, item)
			return nil
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, collections)
	})
}
