//go:build linux || darwin

package executionbudget

import (
	"runtime"
	"syscall"
)

func hostMeasurement() measurementSupport {
	return measurementSupport{
		cpu: true, memory: true,
		cpuDetail:  "CPU time is measured as runner process user plus system time (self and reaped children) during the attempt; concurrent work in the same process is included",
		memoryNote: "memory is measured as the runner process peak resident set size high-water mark, which includes usage before the attempt",
	}
}

func hostSample() sample {
	var self, children syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &self) != nil || syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children) != nil {
		return sample{}
	}
	cpu := func(usage syscall.Rusage) int64 {
		return (usage.Utime.Nano() + usage.Stime.Nano()) / 1e6
	}
	peak := int64(self.Maxrss)
	if runtime.GOOS == "linux" {
		peak *= 1024
	}
	return sample{cpuMS: cpu(self) + cpu(children), peakRSS: peak, cpu: true, memory: true}
}
