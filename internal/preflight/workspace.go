package preflight

import (
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/discovery"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
)

func applyWorkspaceSkillDefaults(r *Report, spec *models.EvalSpec) {
	if spec.Config.AllSkillsDisabled() || len(spec.Config.SkillPaths) > 0 ||
		(spec.Scenario != "" && spec.SkillName == "") {
		return
	}
	cfg, err := projectconfig.Load(".")
	if err != nil {
		r.add("workspace.configuration", Unresolved, r.Source, "", "", "Project workspace defaults cannot be read; runtime would fall back to its standard defaults.", "Correct the local .waza.yaml before relying on discovered skill prerequisites.")
		r.Complete = false
		return
	}
	if cfg.Dir == "" {
		return
	}
	root := filepath.Join(cfg.Dir, cfg.Paths.Skills)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		r.add("workspace.skills", Unresolved, r.Source, "", "", "Configured workspace skills folder is unavailable; runtime skips its default skill discovery.", "Check the project skills folder or configure explicit eval skill_directories.")
		return
	}
	paths, err := discovery.SkillDirectories(root)
	if err != nil {
		r.add("workspace.discovery", Invalid, r.Source, "", "", "Workspace skill directories cannot be discovered using runtime rules.", "Check local project/skill paths and filesystem access.")
		r.Complete = false
		return
	}
	spec.Config.SkillPaths = paths
}
