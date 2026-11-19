package artifactmanifest

import (
	"bytes"
	"context"
	"fmt"
)

// ReadBackend permits bounded object reads without granting writes.
type ReadBackend interface {
	Get(context.Context, string, int64) ([]byte, error)
}

// NamespaceReader exposes only current raw reads from an admitted namespace.
// Its decorator can reserve a shared byte budget before provider I/O.
type NamespaceReader struct{ backend ReadBackend }

// Reader passes a scoped read-only capability to decorate, never the underlying
// provider. Get accepts the same logical keys as ReadUnverified and at most the
// namespace object bound; provider reads may consume one excess-detection byte.
func (n *Namespace) Reader(decorate func(ReadBackend) ReadBackend) (*NamespaceReader, error) {
	if n == nil || n.backend == nil {
		return nil, ErrUnsupportedBackend
	}
	if decorate == nil {
		return nil, fmt.Errorf("%w: namespace read decorator is required", ErrInvalid)
	}
	reader := decorate(namespaceReadBackend{backend: n.backend})
	if reader == nil {
		return nil, fmt.Errorf("%w: namespace read decorator returned no reader", ErrInvalid)
	}
	return &NamespaceReader{backend: reader}, nil
}

type namespaceReadBackend struct{ backend objectBackend }

func (r namespaceReadBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	physical, err := namespaceKey(key)
	if err != nil {
		return nil, err
	}
	if limit < 0 || limit > MaxNamespaceObjectBytes {
		return nil, ErrInvalid
	}
	return r.backend.readObject(ctx, physical, limit)
}

// ReadUnverified returns raw bytes only; canonical profile, context and digest
// admission remain the caller's responsibility.
func (r *NamespaceReader) ReadUnverified(ctx context.Context, key string) ([]byte, error) {
	if _, err := namespaceKey(key); err != nil {
		return nil, err
	}
	if r == nil || r.backend == nil {
		return nil, ErrUnsupportedBackend
	}
	data, err := r.backend.Get(ctx, key, MaxNamespaceObjectBytes)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxNamespaceObjectBytes {
		return nil, ErrCorrupt
	}
	return bytes.Clone(data), nil
}
