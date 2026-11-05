package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecoveryMCPProcessBudgets(t *testing.T) {
	startFailure := errors.New("injected process start failure")
	for _, test := range []struct {
		name         string
		delay        time.Duration
		startErr     error
		cancelParent bool
		wantErr      error
	}{
		{name: "ordinary start"},
		{name: "delayed start", delay: 14 * time.Second},
		{name: "startup deadline", delay: 31 * time.Second, wantErr: context.DeadlineExceeded},
		{name: "start failure", startErr: startFailure, wantErr: startFailure},
		{name: "parent cancellation", delay: 14 * time.Second, cancelParent: true, wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancelParent := context.WithCancel(t.Context())
				defer cancelParent()
				sibling, cancelSibling := context.WithCancel(parent)
				defer cancelSibling()
				child, cancelChild := context.WithCancel(parent)
				defer cancelChild()
				var cancellations atomic.Int32
				ctx, stop, err := startRecoveryMCPProcess(parent, func() error {
					if test.cancelParent {
						time.AfterFunc(5*time.Second, cancelParent)
					}
					time.Sleep(test.delay)
					return test.startErr
				}, func() {
					cancellations.Add(1)
					cancelChild()
				})
				if test.wantErr != nil {
					require.ErrorIs(t, err, test.wantErr)
					require.Nil(t, ctx, "a failed start must not admit an exchange")
					require.Nil(t, stop)
					require.ErrorIs(t, child.Err(), context.Canceled)
					if !test.cancelParent {
						require.NoError(t, parent.Err(), "startup failure must not cancel the caller")
						require.NoError(t, sibling.Err(), "startup failure must not cancel unrelated work")
					}
					return
				}
				require.NoError(t, err)
				defer stop()
				require.NoError(t, child.Err(), "process creation consumed the exchange budget")
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				require.Equal(t, 10*time.Second, time.Until(deadline))
				stop()
				require.ErrorIs(t, child.Err(), context.Canceled)
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				time.Sleep(11 * time.Second)
				synctest.Wait()
				require.Equal(t, int32(1), cancellations.Load(), "cleanup must stop the exchange deadline callback")
				require.NoError(t, parent.Err())
				require.NoError(t, sibling.Err())
			})
		})
	}
}

func TestRecoveryMCPProcessCleanupJoinsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()
		child, cancelChild := context.WithCancel(parent)
		defer cancelChild()
		entered, release := make(chan struct{}), make(chan struct{})
		var cancellations atomic.Int32
		ctx, stop, err := startRecoveryMCPProcess(parent, func() error { return nil }, func() {
			if cancellations.Add(1) == 1 {
				close(entered)
				<-release
			}
			cancelChild()
		})
		require.NoError(t, err)
		<-entered
		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		synctest.Wait()
		returnedEarly := false
		select {
		case <-stopped:
			returnedEarly = true
		default:
		}
		close(release)
		<-stopped
		require.False(t, returnedEarly, "cleanup returned before its cancellation callback joined")
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		require.ErrorIs(t, child.Err(), context.Canceled)
		require.NoError(t, parent.Err())
	})
}
