//go:build windows

package executionbudget

import "golang.org/x/sys/windows"

func hostMeasurement() measurementSupport {
	return measurementSupport{
		cpu:       true,
		cpuDetail: "CPU time is measured as runner process kernel plus user time during the attempt; child processes and concurrent work are not separated",
	}
}

func hostSample() sample {
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(windows.CurrentProcess(), &creation, &exit, &kernel, &user) != nil {
		return sample{}
	}
	ticks := func(t windows.Filetime) int64 { return int64(t.HighDateTime)<<32 | int64(t.LowDateTime) }
	// FILETIME durations are 100ns ticks.
	return sample{cpuMS: (ticks(kernel) + ticks(user)) / 10_000, cpu: true}
}
