package adversarial

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackTaskRelPaths(t *testing.T) {
	p := &Pack{Manifest: Manifest{Tasks: []string{"a/b.yaml", "c.yaml"}}}
	got := p.TaskRelPaths("/root")
	require.Equal(t, []string{filepath.Join("/root", "a", "b.yaml"), filepath.Join("/root", "c.yaml")}, got)

	empty := &Pack{}
	require.Empty(t, empty.TaskRelPaths("/x"))
}
