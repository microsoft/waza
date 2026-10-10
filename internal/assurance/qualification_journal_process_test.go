package assurance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQualificationRegistryIndependentProcessClaim(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("physical local registry unsupported")
	}
	if path := os.Getenv("QUALIFICATION_CLAIM_CHILD"); path != "" {
		manifest, _ := qualificationProtocolFixture(t)
		prototype, _, _ := qualificationLocalTestJournal(t, manifest)
		root, err := os.OpenRoot(path)
		require.NoError(t, err)
		defer func() { require.NoError(t, root.Close()) }()
		config := prototype.config
		config.Root = root
		journal, err := qualificationOpenLocalJournal(t.Context(), config)
		require.NoError(t, err)
		defer func() { require.NoError(t, journal.Close()) }()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		_, err = journal.Claim(ctx, manifest)
		if err == nil {
			fmt.Println("QUALIFICATION_CLAIM_WON")
		} else {
			fmt.Println("QUALIFICATION_CLAIM_BLOCKED")
		}
		return
	}
	directory := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	commands := []*exec.Cmd{}
	for range 2 {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQualificationRegistryIndependentProcessClaim$", "-test.timeout=10s")
		command.Env = append(os.Environ(), "QUALIFICATION_CLAIM_CHILD="+directory)
		commands = append(commands, command)
	}
	results := make(chan []byte, 2)
	for _, command := range commands {
		go func() {
			output, err := command.CombinedOutput()
			if err != nil {
				results <- append([]byte("FAILED: "), output...)
				return
			}
			results <- output
		}()
	}
	won, blocked := 0, 0
	for range 2 {
		output := string(<-results)
		require.NotContains(t, output, "FAILED:")
		if strings.Contains(output, "QUALIFICATION_CLAIM_WON") {
			won++
		}
		if strings.Contains(output, "QUALIFICATION_CLAIM_BLOCKED") {
			blocked++
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, blocked)
}
