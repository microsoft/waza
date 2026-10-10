package assurance

import (
	"os"

	"github.com/microsoft/waza/internal/rootedfile"
)

func openReferenceDocument(root *os.Root, name string) (*os.File, error) {
	return rootedfile.Open(root, name)
}
