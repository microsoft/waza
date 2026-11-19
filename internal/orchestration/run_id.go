package orchestration

import (
	"fmt"
	"sync/atomic"
	"time"
)

var lastRunIDTimestamp atomic.Int64

func newRunID(now time.Time) string {
	candidate := now.UnixNano()
	for {
		previous := lastRunIDTimestamp.Load()
		if candidate <= previous {
			candidate = previous + 1
		}
		if lastRunIDTimestamp.CompareAndSwap(previous, candidate) {
			return fmt.Sprintf("run-%d", candidate)
		}
	}
}
