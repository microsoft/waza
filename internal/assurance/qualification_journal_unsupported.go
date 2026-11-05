//go:build !linux && !darwin

package assurance

import (
	"context"
	"errors"
	"os"
)

var errQualificationPlatform = errors.New("qualification: local physical filesystem backend unsupported on this platform")

func qualificationSecureRead(context.Context, *os.Root, string, uint64) ([]byte, error) {
	return nil, errQualificationPlatform
}
func (qualificationOSJournalIO) Lock(context.Context, *os.Root) (qualificationRegistryLock, error) {
	return nil, errQualificationPlatform
}
func (qualificationOSJournalIO) CreateDirectDirectory(context.Context, *os.Root, string) (*os.Root, error) {
	return nil, errQualificationPlatform
}
func (qualificationOSJournalIO) OpenDirectDirectory(context.Context, *os.Root, string) (*os.Root, error) {
	return nil, errQualificationPlatform
}
func (qualificationOSJournalIO) ReadDirect(context.Context, *os.Root, string, uint64) ([]byte, error) {
	return nil, errQualificationPlatform
}
func (qualificationOSJournalIO) WriteExclusive(context.Context, *os.Root, string, []byte) error {
	return errQualificationPlatform
}
func (qualificationOSJournalIO) SyncFile(context.Context, *os.Root, string) error {
	return errQualificationPlatform
}
func (qualificationOSJournalIO) SyncDirectory(context.Context, *os.Root) error {
	return errQualificationPlatform
}
