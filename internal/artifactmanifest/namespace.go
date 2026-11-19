package artifactmanifest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	ExecutionLedgerNamespaceV1 = "execution-ledger-v1"
	MaxNamespaceObjectBytes    = 1 << 20
	namespacePrefix            = ExecutionLedgerNamespaceV1 + "-"
)

var (
	ErrOccupied           = errors.New("conditional namespace object is occupied")
	ErrUnsupportedBackend = errors.New("conditional namespace backend is unsupported")
)

type objectBackend interface {
	createObject(context.Context, string, []byte) (bool, error)
	readObject(context.Context, string, int64) ([]byte, error)
	listObjects(context.Context, string) ([]string, bool, error)
	listAllObjects(context.Context, string, int) ([]string, bool, error)
}

// Namespace stores opaque append-only records for cooperating API writers.
// It does not validate sidecars, grant execution ownership, or make creation of
// multiple objects a transaction. Reads and discovery are explicitly unverified.
type Namespace struct{ backend objectBackend }

func NewNamespace(backend Backend, name string) (*Namespace, error) {
	if name != ExecutionLedgerNamespaceV1 {
		return nil, fmt.Errorf("%w: namespace is not admitted", ErrInvalid)
	}
	switch provider := backend.(type) {
	case *Local:
		if provider != nil && provider.root != nil {
			return &Namespace{backend: provider}, nil
		}
	case *Azure:
		if provider != nil && provider.client != nil {
			return &Namespace{backend: provider}, nil
		}
	}
	return nil, ErrUnsupportedBackend
}

func namespaceKey(logical string) (string, error) {
	kind, id, found := strings.Cut(logical, "/")
	if !found {
		return "", ErrInvalid
	}
	var prefix string
	switch kind {
	case "reservation", "completion":
		prefix = "exec-"
	case "decision":
		prefix = "decision-"
	default:
		return "", ErrInvalid
	}
	if !strings.HasPrefix(id, prefix) || !validDigest(strings.TrimPrefix(id, prefix)) {
		return "", ErrInvalid
	}
	return namespacePrefix + kind + "-" + id, nil
}

// Create succeeds only for a newly created and fully reread object. Occupied
// records return ErrOccupied even for identical bytes; they grant no ownership.
// A failed response/readback may leave a committed record, never a success.
func (n *Namespace) Create(ctx context.Context, key string, data []byte) (bool, error) {
	physical, err := namespaceKey(key)
	if err != nil {
		return false, err
	}
	if len(data) > MaxNamespaceObjectBytes {
		return false, ErrInvalid
	}
	if n == nil || n.backend == nil {
		return false, ErrUnsupportedBackend
	}
	original := slices.Clone(data)
	created, err := n.backend.createObject(ctx, physical, original)
	if err != nil {
		return false, fmt.Errorf("creating conditional namespace object: %w", err)
	}
	if !created {
		return false, ErrOccupied
	}
	actual, err := n.backend.readObject(ctx, physical, int64(len(original)))
	if err != nil {
		return false, fmt.Errorf("verifying conditional namespace creation: %w", err)
	}
	if err := verifyBytes(actual, Hash(original), int64(len(original))); err != nil {
		return false, err
	}
	if !bytes.Equal(actual, original) {
		return false, ErrConflict
	}
	return true, nil
}

// ReadUnverified returns detached current bytes. The caller must validate its
// own canonical profile, context, and any independently bound content digest.
func (n *Namespace) ReadUnverified(ctx context.Context, key string) ([]byte, error) {
	physical, err := namespaceKey(key)
	if err != nil {
		return nil, err
	}
	if n == nil || n.backend == nil {
		return nil, ErrUnsupportedBackend
	}
	return n.backend.readObject(ctx, physical, MaxNamespaceObjectBytes)
}

// ListUnverified returns bounded sorted logical keys, not validated sidecars or
// a global ordering. Truncation cannot be treated as complete logical discovery.
func (n *Namespace) ListUnverified(ctx context.Context) ([]string, bool, error) {
	if n == nil || n.backend == nil {
		return nil, false, ErrUnsupportedBackend
	}
	physical, truncated, err := n.backend.listObjects(ctx, namespacePrefix)
	if err != nil {
		return nil, false, err
	}
	return logicalKeys(physical, truncated)
}

// ListAllUnverified follows provider pagination and returns at most limit
// sorted logical keys. Truncation still cannot be treated as complete.
func (n *Namespace) ListAllUnverified(ctx context.Context, limit int) ([]string, bool, error) {
	if n == nil || n.backend == nil {
		return nil, false, ErrUnsupportedBackend
	}
	if limit < 1 {
		return nil, false, ErrInvalid
	}
	physical, truncated, err := n.backend.listAllObjects(ctx, namespacePrefix, limit)
	if err != nil {
		return nil, false, err
	}
	return logicalKeys(physical, truncated)
}

func logicalKeys(physical []string, truncated bool) ([]string, bool, error) {
	keys := make([]string, 0, len(physical))
	for _, key := range physical {
		rest, found := strings.CutPrefix(key, namespacePrefix)
		kind, id, separated := strings.Cut(rest, "-")
		logical := kind + "/" + id
		mapped, mapErr := namespaceKey(logical)
		if !found || !separated || mapErr != nil || mapped != key {
			return nil, false, fmt.Errorf("%w: invalid stored namespace key", ErrCorrupt)
		}
		keys = append(keys, logical)
	}
	slices.Sort(keys)
	return keys, truncated, nil
}
