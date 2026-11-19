package webapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func BenchmarkMarshalRunSummaries5000(b *testing.B) {
	runs := make([]RunSummary, 5000)
	skill := "skill"
	for i := range runs {
		runs[i] = RunSummary{
			ID:              fmt.Sprintf("run-%d", i),
			Spec:            fmt.Sprintf("eval-%d", i%25),
			Model:           fmt.Sprintf("model-%d", i%8),
			Outcome:         "passed",
			PassCount:       10,
			TaskCount:       10,
			Tokens:          1000 + i,
			PremiumRequests: 1,
			Cost:            0.1,
			Duration:        30,
			Timestamp:       time.Unix(int64(i), 0).UTC(),
			Skill:           &skill,
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := json.Marshal(runs); err != nil {
			b.Fatal(err)
		}
	}
}
