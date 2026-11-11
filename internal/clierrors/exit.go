package clierrors

import "fmt"

// ExitCodeError lets a command request a specific process exit code while
// still returning an error for the CLI entry point to report.
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit code %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error {
	return e.Err
}
