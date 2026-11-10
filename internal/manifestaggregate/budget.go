package manifestaggregate

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

type readBudget struct {
	remainingObjects int
	remainingBytes   int64
}

func (b *readBudget) read(ctx context.Context, limit int64, read func() ([]byte, error)) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reserve the full bounded read, including excess detection, before I/O.
	if limit < 0 || b.remainingObjects <= 0 || limit >= b.remainingBytes {
		return nil, ErrBudget
	}
	b.remainingObjects--
	b.remainingBytes -= limit + 1
	data, err := read()
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: provider exceeded read bound", artifactmanifest.ErrCorrupt)
	}
	if err != nil {
		// An error need not return the bytes already consumed by a provider.
		// Keep its complete reservation instead of inventing a zero-byte read.
		return nil, err
	}
	b.remainingBytes += limit + 1 - int64(len(data))
	return data, nil
}

type readOnlyBackend struct {
	source artifactmanifest.ReadBackend
	budget *readBudget
}

func (b readOnlyBackend) Get(ctx context.Context, key string, limit int64) ([]byte, error) {
	return b.budget.read(ctx, limit, func() ([]byte, error) { return b.source.Get(ctx, key, limit) })
}

func (readOnlyBackend) Create(context.Context, string, []byte) (bool, error) {
	return false, fmt.Errorf("%w: aggregation has no object write capability", ErrInvalid)
}
