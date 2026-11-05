package nativesnapshot

import (
	"context"

	"github.com/microsoft/waza/internal/nativetask"
)

const (
	CollectionExecutableBytes = nativetask.CollectionExecutableBytes
	CollectionSourceBytes     = nativetask.CollectionSourceBytes
	CollectionWireBytes       = 1 << 30
)

type collectionLimitsKey struct{}

func collectionContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, collectionLimitsKey{}, true)
}

func sourceByteLimits(ctx context.Context) (executable, total int) {
	if ctx != nil {
		if selected, _ := ctx.Value(collectionLimitsKey{}).(bool); selected {
			return CollectionExecutableBytes, CollectionSourceBytes
		}
	}
	return maxExecutableBytes, maxTotalBytes
}
