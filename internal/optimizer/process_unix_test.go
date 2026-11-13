//go:build !windows

package optimizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommandAdapterTimeoutKillsDescendants(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv(helperPIDFileEnv, pidFile)
	_, err := helperAdapter(t, "spawn", 2*time.Second, helperPIDFileEnv).Propose(context.Background(), proposeReq())
	require.Equal(t, CategoryOptimizerFailure, CategoryOf(err), err)

	data, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	}, 10*time.Second, 50*time.Millisecond, "grandchild %d survived adapter timeout", pid)
}
