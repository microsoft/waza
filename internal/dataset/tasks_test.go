package dataset

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLoadTasksPreservesFallbacksAndSharedTemplateContext(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "data.csv", "id,name,prompt,value\nexplicit,Named,{{.Vars.value}} {{.JobID}},row-value\n,NameOnly,{{.TaskName}},\n,,{{.Timestamp}},\n")
	spec := &models.EvalSpec{TasksFrom: "data.csv", Inputs: map[string]string{"value": "default-value"}}
	now := time.Unix(123, 0).UTC()
	tasks, err := LoadTasks(spec, dir, now)
	require.NoError(t, err)
	require.Len(t, tasks, 3)
	require.Equal(t, "explicit", tasks[0].TestID)
	require.Equal(t, "Named", tasks[0].DisplayName)
	require.Equal(t, "row-value run-123", tasks[0].Stimulus.Message)
	require.Equal(t, "NameOnly", tasks[1].TestID)
	require.Equal(t, "NameOnly", tasks[1].Stimulus.Message)
	require.Equal(t, "row-3", tasks[2].TestID)
	require.Equal(t, now.Format(time.RFC3339), tasks[2].Stimulus.Message)
	spec.Range = [2]int{2, 2}
	tasks, err = LoadTasks(spec, dir, now)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "NameOnly", tasks[0].TestID)
}

func TestLoadTasksRangedFallbackIdentityUsesOriginalRow(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "data.csv", "id,name,prompt\nexplicit,Named,{{.TaskName}}\n,,{{.TaskName}}\n,,{{.TaskName}}\n")
	spec := &models.EvalSpec{TasksFrom: "data.csv"}
	now := time.Unix(0, 0)
	full, err := LoadTasks(spec, dir, now)
	require.NoError(t, err)
	for _, bounds := range [][2]int{{2, 2}, {2, 3}, {3, 99}} {
		spec.Range = bounds
		selected, err := LoadTasks(spec, dir, now)
		require.NoError(t, err)
		require.NotEmpty(t, selected)
		for i, task := range selected {
			require.Equal(t, full[bounds[0]-1+i], task)
		}
	}
	spec.Range = [2]int{4, 99}
	selected, err := LoadTasks(spec, dir, now)
	require.NoError(t, err)
	require.Empty(t, selected)
	spec.Range = [2]int{2, 2}
	writeCSV(t, dir, "data.csv", "prompt\nfirst\n{{bad\n")
	_, err = LoadTasks(spec, dir, now)
	require.ErrorContains(t, err, "resolving prompt template for row 2")
}

func TestLoadTasksResolvedSymlinkContainment(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	writeCSV(t, dir, "local.csv", "prompt\nlocal\n")
	writeCSV(t, outside, "outside.csv", "prompt\noutside-secret\n")
	for _, tc := range []struct {
		name, target string
		escape       bool
	}{
		{"internal", filepath.Join(dir, "local.csv"), false},
		{"external", filepath.Join(outside, "outside.csv"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(dir, tc.name+".csv")
			require.NoError(t, os.Symlink(tc.target, link))
			for _, bounds := range [][2]int{{}, {1, 1}} {
				tasks, err := LoadTasks(&models.EvalSpec{TasksFrom: filepath.Base(link), Range: bounds}, dir, time.Unix(0, 0))
				if tc.escape {
					require.ErrorContains(t, err, "escapes spec directory through symlinks")
					require.Nil(t, tasks)
				} else {
					require.NoError(t, err)
					require.Equal(t, "local", tasks[0].Stimulus.Message)
				}
			}
		})
	}
	alias := filepath.Join(t.TempDir(), "suite")
	require.NoError(t, os.Symlink(dir, alias))
	tasks, err := LoadTasks(&models.EvalSpec{TasksFrom: "local.csv"}, alias, time.Unix(0, 0))
	require.NoError(t, err)
	require.Equal(t, "local", tasks[0].Stimulus.Message)
	tasks, err = LoadTasks(&models.EvalSpec{TasksFrom: filepath.Join(dir, "local.csv")}, alias, time.Unix(0, 0))
	require.ErrorContains(t, err, "escapes spec directory")
	require.Nil(t, tasks)
	for _, tc := range []struct {
		name, target, csv string
		escape            bool
	}{
		{"internal-dir", dir, "local.csv", false},
		{"external-dir", outside, "outside.csv", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(dir, tc.name)
			require.NoError(t, os.Symlink(tc.target, link))
			tasks, err := LoadTasks(&models.EvalSpec{TasksFrom: filepath.Join(tc.name, tc.csv)}, dir, time.Unix(0, 0))
			if tc.escape {
				require.ErrorContains(t, err, "escapes spec directory through symlinks")
				require.Nil(t, tasks)
			} else {
				require.NoError(t, err)
				require.Equal(t, "local", tasks[0].Stimulus.Message)
			}
		})
	}
	require.NoError(t, os.Symlink(filepath.Join(dir, "absent.csv"), filepath.Join(dir, "dangling.csv")))
	_, err = LoadTasks(&models.EvalSpec{TasksFrom: "dangling.csv"}, dir, time.Unix(0, 0))
	require.ErrorContains(t, err, "loading CSV dataset")
}

func TestLoadTasksErrors(t *testing.T) {
	for _, tc := range []struct {
		name, path, csv, want string
		bounds                [2]int
	}{
		{name: "missing", path: "absent.csv", want: "loading CSV dataset"},
		{name: "escaping", path: "../data.csv", want: "escapes spec directory"},
		{name: "range zero", path: "data.csv", bounds: [2]int{0, 1}, want: "both values must be > 0"},
		{name: "range reversed", path: "data.csv", bounds: [2]int{2, 1}, want: "must be <= end"},
		{name: "malformed template", path: "data.csv", csv: "prompt\n{{bad\n", want: "resolving prompt template"},
		{name: "invalid CSV", path: "data.csv", csv: "prompt\n\"unclosed\n", want: "loading CSV dataset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.csv != "" {
				writeCSV(t, dir, "data.csv", tc.csv)
			}
			_, err := LoadTasks(&models.EvalSpec{TasksFrom: tc.path, Range: tc.bounds}, dir, time.Unix(0, 0))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadTasksFilesystemRootContainment(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, dir, "data.csv", "prompt\nroot-contained\n")
	root := filepath.VolumeName(dir) + string(filepath.Separator)
	path := filepath.Join(dir, "data.csv")
	relative, err := filepath.Rel(root, path)
	require.NoError(t, err)
	for _, input := range []string{path, relative} {
		tasks, err := LoadTasks(&models.EvalSpec{TasksFrom: input}, root, time.Unix(0, 0))
		require.NoError(t, err)
		require.Len(t, tasks, 1)
		require.Equal(t, "root-contained", tasks[0].Stimulus.Message)
	}
}
