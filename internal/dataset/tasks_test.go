package dataset

import (
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
