package manifestaggregate

import (
	"errors"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

// OpenLocal opens an explicitly selected existing public store. It does not
// create directories, inspect ambient configuration, or construct credentials.
func OpenLocal(directory string) (Sources, func() error, error) {
	backend, err := artifactmanifest.OpenLocal(directory)
	if err != nil {
		return Sources{}, nil, err
	}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		return Sources{}, nil, errors.Join(err, backend.Close())
	}
	reader, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
	if err != nil {
		return Sources{}, nil, errors.Join(err, backend.Close())
	}
	return Sources{Objects: backend, Namespace: reader}, backend.Close, nil
}
