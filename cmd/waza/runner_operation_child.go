package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/runnercheckpoint"
)

const (
	runnerOperationWriterMode = "__waza_internal_runner_operation_writer"
	runnerOperationWriterEnv  = "WAZA_INTERNAL_RUNNER_OPERATION_WRITER"
	runnerOperationWriterMax  = runnercheckpoint.MaxResultBytes + 1
)

func init() {
	if len(os.Args) != 3 || os.Args[1] != runnerOperationWriterMode {
		return
	}
	token := os.Getenv(runnerOperationWriterEnv)
	decodedToken, err := hex.DecodeString(token)
	if err != nil || len(decodedToken) != 16 || hex.EncodeToString(decodedToken) != token {
		return
	}

	size, err := strconv.Atoi(os.Args[2])
	if err != nil || strconv.Itoa(size) != os.Args[2] || size < 0 || size > runnerOperationWriterMax {
		os.Exit(2)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(os.Stdin, data); err != nil {
		os.Exit(2)
	}
	written, err := os.Stderr.Write([]byte{1})
	if err != nil || written != 1 {
		os.Exit(2)
	}
	written, err = os.Stdout.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func writeRunnerOperationOutputChild(output *os.File, data []byte, timeout time.Duration) error {
	if len(data) > runnerOperationWriterMax {
		return fmt.Errorf("runner operation output exceeds %d bytes", runnerOperationWriterMax)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating runner operation output writer: %w", err)
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return fmt.Errorf("creating runner operation output writer token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])
	command := exec.Command(executable, runnerOperationWriterMode, strconv.Itoa(len(data)))
	command.Env = runnerOperationWriterEnvironment(token)
	command.Stdin = bytes.NewReader(data)
	command.Stdout = output
	control, err := command.StderrPipe()
	if err != nil {
		return fmt.Errorf("creating runner operation output control pipe: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	if err := command.Start(); err != nil {
		return errors.Join(fmt.Errorf("starting runner operation output writer: %w", err), control.Close())
	}

	ready := make(chan error, 1)
	go func() {
		var marker [1]byte
		_, readErr := io.ReadFull(control, marker[:])
		if readErr == nil && marker[0] != 1 {
			readErr = errors.New("invalid runner operation output writer readiness marker")
		}
		ready <- readErr
	}()

	select {
	case err := <-ready:
		if err != nil {
			killErr := command.Process.Kill()
			waitErr := command.Wait()
			if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				err = errors.Join(err, fmt.Errorf("stopping runner operation output writer: %w", killErr))
			}
			return fmt.Errorf("starting runner operation output writer: %w", errors.Join(err, waitErr))
		}
	case <-timer.C:
		killErr := command.Process.Kill()
		readyErr := <-ready
		waitErr := command.Wait()
		if errors.Is(killErr, os.ErrProcessDone) && readyErr == nil {
			if waitErr != nil {
				return fmt.Errorf("writing runner operation output: %w", waitErr)
			}
			return nil
		}
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(
				fmt.Errorf("starting runner operation output writer: %w", os.ErrDeadlineExceeded),
				fmt.Errorf("stopping runner operation output writer: %w", killErr),
				waitErr,
			)
		}
		return fmt.Errorf("starting runner operation output writer: %w", os.ErrDeadlineExceeded)
	}

	waitDone := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		if waitErr != nil {
			return fmt.Errorf("writing runner operation output: %w", waitErr)
		}
		return nil
	case <-timer.C:
		select {
		case <-waitDone:
			if waitErr != nil {
				return fmt.Errorf("writing runner operation output: %w", waitErr)
			}
			return nil
		default:
		}
	}

	killErr := command.Process.Kill()
	<-waitDone
	if errors.Is(killErr, os.ErrProcessDone) {
		if waitErr != nil {
			return fmt.Errorf("writing runner operation output: %w", waitErr)
		}
		return nil
	}
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return errors.Join(
			fmt.Errorf("writing runner operation output: %w", os.ErrDeadlineExceeded),
			fmt.Errorf("stopping runner operation output writer: %w", killErr),
			waitErr,
		)
	}
	return fmt.Errorf("writing runner operation output: %w", os.ErrDeadlineExceeded)
}

func runnerOperationWriterEnvironment(token string) []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, runnerOperationWriterEnv) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, runnerOperationWriterEnv+"="+token)
}
