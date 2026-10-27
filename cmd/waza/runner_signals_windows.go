//go:build windows

package main

import "os"

func runnerSignals() []os.Signal { return []os.Signal{os.Interrupt} }
