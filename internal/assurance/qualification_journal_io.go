package assurance

import (
	"context"
	"os"
)

type qualificationJournalIO interface {
	Lock(context.Context, *os.Root) (qualificationRegistryLock, error)
	CreateDirectDirectory(context.Context, *os.Root, string) (*os.Root, error)
	OpenDirectDirectory(context.Context, *os.Root, string) (*os.Root, error)
	ReadDirect(context.Context, *os.Root, string, uint64) ([]byte, error)
	WriteExclusive(context.Context, *os.Root, string, []byte) error
	SyncFile(context.Context, *os.Root, string) error
	SyncDirectory(context.Context, *os.Root) error
}
type qualificationRegistryLock interface{ Unlock() error }
type qualificationOSJournalIO struct{}

// Read-only recovery acquires an existing process lock; it never initializes
// a registry namespace. The mode is forwarded through injected I/O wrappers.
type qualificationReadLockKey struct{}
