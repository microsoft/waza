package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

// Only inspect local definitions; never construct or initialize an engine.
func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: skill-discovery <generated-spec>")
	}
	spec, err := models.LoadEvalSpec(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	cfg := config.NewEvalConfig(spec, config.WithSpecDir(filepath.Dir(os.Args[1])))
	paths := utils.ResolvePaths(cfg.Spec().Config.FilteredSkillPaths(), cfg.SpecDir())
	if len(paths) != 1 || !execution.IsSkillAvailable(paths, spec.SkillName) {
		log.Fatal("declared skill was not found through the generated spec configuration")
	}
	if _, err := fmt.Fprintln(os.Stdout, paths[0]); err != nil {
		log.Fatal(err)
	}
}
