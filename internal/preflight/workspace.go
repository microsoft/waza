package preflight

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/discovery"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
)

func applyWorkspaceSkillDefaults(r *Report, spec *models.EvalSpec, opts Options) {
	if spec.Config.AllSkillsDisabled() || len(spec.Config.SkillPaths) > 0 ||
		(spec.Scenario != "" && strings.TrimSpace(spec.SkillName) == "") {
		return
	}
	root := opts.WorkspaceSkillsDir
	if root == "" {
		cfg, err := projectconfig.Load(".")
		if err != nil {
			r.add("workspace.configuration", Unresolved, r.Source, "", "", "Project workspace defaults cannot be read; runtime would fall back to its standard defaults.", "Correct the local .waza.yaml before relying on discovered skill prerequisites.")
			r.Complete = false
			return
		}
		if cfg.Dir != "" {
			root = filepath.Join(cfg.Dir, cfg.Paths.Skills)
		}
	}
	var paths []string
	if root != "" {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			message := "Configured workspace skills folder is unavailable; runtime skips its default skill discovery."
			if opts.WorkspaceSkillsDir != "" {
				message = "Selected workspace skills folder is unavailable; preflight cannot discover its default skills."
			}
			r.add("workspace.skills", Unresolved, r.Source, "", "", message, "Check the project skills folder or configure explicit eval skill_directories.")
		} else {
			paths, err = discovery.SkillDirectories(root)
			if err != nil {
				r.add("workspace.discovery", Invalid, r.Source, "", "", "Workspace skill directories cannot be discovered using runtime rules.", "Check local project/skill paths and filesystem access.")
				r.Complete = false
				return
			}
		}
	}
	for _, dir := range opts.DefaultSkillPaths {
		if !slices.Contains(paths, dir) {
			paths = append(paths, dir)
		}
	}
	spec.Config.SkillPaths = paths
}
