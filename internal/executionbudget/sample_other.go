//go:build !linux && !darwin && !windows

package executionbudget

func hostMeasurement() measurementSupport { return measurementSupport{} }

func hostSample() sample { return sample{} }
