package dataset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/template"
)

// LoadTasks preserves the runner's CSV selection, IDs, and template semantics.
// Callers supply the clock so offline plans can be deterministic.
func LoadTasks(spec *models.EvalSpec, baseDir string, now time.Time) ([]*models.TestCase, error) {
	if baseDir == "" {
		baseDir = "."
	}
	csvPath := spec.TasksFrom
	if !filepath.IsAbs(csvPath) {
		csvPath = filepath.Join(baseDir, csvPath)
	}
	absBaseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory: %w", err)
	}
	absCSVPath, err := filepath.Abs(csvPath)
	if err != nil {
		return nil, fmt.Errorf("resolving CSV path: %w", err)
	}
	lexicalRelative, err := filepath.Rel(absBaseDir, absCSVPath)
	if err != nil {
		return nil, fmt.Errorf("checking CSV path containment: %w", err)
	}
	if lexicalRelative == "." || !filepath.IsLocal(lexicalRelative) {
		return nil, fmt.Errorf("tasks_from path %q escapes spec directory", spec.TasksFrom)
	}
	if spec.Range != [2]int{} {
		if spec.Range[0] <= 0 || spec.Range[1] <= 0 {
			return nil, fmt.Errorf("invalid range: both values must be > 0, got [%d, %d]", spec.Range[0], spec.Range[1])
		}
		if spec.Range[0] > spec.Range[1] {
			return nil, fmt.Errorf("invalid range: start (%d) must be <= end (%d)", spec.Range[0], spec.Range[1])
		}
	}
	rows, err := loadTaskRows(absBaseDir, absCSVPath, spec.TasksFrom)
	if err != nil {
		return nil, fmt.Errorf("loading CSV dataset: %w", err)
	}
	if spec.Range != [2]int{} {
		rows = selectCSVRange(rows, spec.Range[0], spec.Range[1])
	}
	tasks := make([]*models.TestCase, 0, len(rows))
	for i, row := range rows {
		rowNum := i + 1
		if spec.Range != [2]int{} {
			rowNum += spec.Range[0] - 1
		}
		id := fmt.Sprintf("row-%d", rowNum)
		if row["id"] != "" {
			id = row["id"]
		} else if row["name"] != "" {
			id = row["name"]
		}
		name := fmt.Sprintf("row-%d", rowNum)
		if row["name"] != "" {
			name = row["name"]
		}
		ctx := &template.Context{
			JobID: fmt.Sprintf("run-%d", now.Unix()), TaskName: name,
			Timestamp: now.Format(time.RFC3339), Vars: make(map[string]string),
		}
		for k, v := range spec.Inputs {
			ctx.Vars[k] = v
		}
		for k, v := range row {
			ctx.Vars[k] = v
		}
		prompt := row["prompt"]
		if strings.Contains(prompt, "{{") {
			prompt, err = template.Render(prompt, ctx)
			if err != nil {
				return nil, fmt.Errorf("resolving prompt template for row %d: %w", rowNum, err)
			}
		}
		tasks = append(tasks, &models.TestCase{
			TestID: id, DisplayName: name, Stimulus: models.TaskStimulus{Message: prompt},
		})
	}
	return tasks, nil
}

func loadTaskRows(base, path, reference string) (rows []Row, err error) {
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("opening spec directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, fmt.Errorf("resolving spec directory symlinks: %w", err)
	}
	resolvedCSV, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolving path symlinks: %w", err)
	}
	relative, err := filepath.Rel(resolvedBase, resolvedCSV)
	if err != nil {
		return nil, fmt.Errorf("checking CSV path containment: %w", err)
	}
	if !filepath.IsLocal(relative) || relative == "." {
		return nil, fmt.Errorf("tasks_from path %q escapes spec directory through symlinks", reference)
	}
	observed, err := os.Stat(resolvedBase)
	if err != nil {
		return nil, fmt.Errorf("checking spec directory identity: %w", err)
	}
	retained, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("checking retained spec directory identity: %w", err)
	}
	if !os.SameFile(observed, retained) {
		return nil, fmt.Errorf("spec directory changed during CSV admission")
	}
	return readCSVAt(root, relative)
}
