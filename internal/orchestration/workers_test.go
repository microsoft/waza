package orchestration

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestResolveWorkers_AutoSizes(t *testing.T) {
	cases := []struct {
		name     string
		req      int
		jobs     int
		wantFunc func(int) bool
	}{
		{"zero jobs returns zero", 0, 0, func(got int) bool { return got == 0 }},
		{"auto-default capped by jobs and CPUs", 0, 2, func(got int) bool { return got >= 1 && got <= 2 }},
		{"auto-default capped by DefaultWorkerCap", 0, 1000, func(got int) bool { return got == DefaultWorkerCap || got == runtime.NumCPU() }},
		{"explicit clamped to jobs", 16, 3, func(got int) bool { return got == 3 }},
		{"explicit honored when within budget", 2, 5, func(got int) bool { return got == 2 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveWorkers(tc.req, tc.jobs, "tasks", nil)
			if !tc.wantFunc(got) {
				t.Fatalf("ResolveWorkers(%d, %d) = %d (unexpected)", tc.req, tc.jobs, got)
			}
		})
	}
}

func TestResolveWorkers_LogsCapNotice(t *testing.T) {
	var buf bytes.Buffer
	got := ResolveWorkers(16, 3, "tasks", &buf)
	if got != 3 {
		t.Fatalf("expected 3, got %d", got)
	}
	if !strings.Contains(buf.String(), "capped to 3") {
		t.Fatalf("expected capping notice, got %q", buf.String())
	}
}

func TestResolveWorkers_NoNoticeWhenWithinBudget(t *testing.T) {
	var buf bytes.Buffer
	_ = ResolveWorkers(2, 10, "tasks", &buf)
	if buf.Len() != 0 {
		t.Fatalf("expected no log output, got %q", buf.String())
	}
}

func TestResolveWorkers_AutoNeverExceedsCap(t *testing.T) {
	got := ResolveWorkers(0, runtime.NumCPU()*4, "tasks", nil)
	if got > DefaultWorkerCap {
		t.Fatalf("auto worker count %d exceeded DefaultWorkerCap %d", got, DefaultWorkerCap)
	}
}
