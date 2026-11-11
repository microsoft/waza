package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func emitInheritedRunnerJSON(data []byte) error {
	var outputHandle windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(os.Stdout.Fd()),
		windows.CurrentProcess(), &outputHandle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return fmt.Errorf("duplicating runner operation output: %w", err)
	}
	output := os.NewFile(uintptr(outputHandle), "runner-operation-output")
	return writeInheritedRunnerJSON(output, data, 5*time.Second)
}

func writeInheritedRunnerJSON(output *os.File, data []byte, timeout time.Duration) (resultErr error) {
	defer func() {
		resultErr = errors.Join(resultErr, output.Close())
	}()
	return writeRunnerOperationOutputChild(output, data, timeout)
}
