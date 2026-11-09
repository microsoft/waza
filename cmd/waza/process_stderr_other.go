//go:build !unix

package main

import "os"

type processStderr struct{}

func (processStderr) Write(data []byte) (int, error) {
	return os.Stderr.Write(data)
}
