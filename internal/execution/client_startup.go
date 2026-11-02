package execution

import (
	"context"
	"errors"
	"sync"
	"time"
)

const startupCleanupTimeout = 2 * time.Second

// startupClient owns the lifetime context, not any individual Start caller.
// Implementations supplied through NewCopilotClient must unblock Start on
// context cancellation (or ForceStop). Cleanup bounds caller waiting, not the
// lifetime of goroutines inside a noncooperating custom implementation.
// The SDK additionally needs ForceStop: Stop takes the same mutex as Start,
// whereas ForceStop kills before locking.
type startupClient struct {
	CopilotClient
	mu         sync.Mutex
	done       chan struct{}
	cancel     context.CancelFunc
	waiters    int
	finished   bool
	closed     bool
	closedDone chan struct{}
	err        error
	stopOnce   sync.Once
	stopDone   chan struct{}
	stopErr    error
}

func (c *startupClient) closeLocked() {
	if !c.closed {
		c.closed = true
		close(c.closedDone)
	}
}

func manageStartup(client CopilotClient) *startupClient {
	if managed, ok := client.(*startupClient); ok {
		return managed
	}
	return &startupClient{CopilotClient: client, stopDone: make(chan struct{}), closedDone: make(chan struct{})}
}

func (c *startupClient) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("Copilot client startup has been closed")
	}
	if c.finished && c.err == nil {
		c.mu.Unlock()
		return nil
	}
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return err
	}
	if c.done == nil {
		lifetime, cancel := context.WithCancel(context.Background())
		c.cancel = cancel
		c.done = make(chan struct{})
		go c.start(lifetime)
	}
	c.waiters++
	done := c.done
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		c.waiters--
		abandon := c.waiters == 0 && !c.finished && !c.closed
		if abandon {
			c.closeLocked()
			c.cancel()
		}
		c.mu.Unlock()
		if abandon {
			c.stop()
			return errors.Join(ctx.Err(), c.waitStop(true))
		}
		return ctx.Err()
	case <-c.closedDone:
		c.mu.Lock()
		c.waiters--
		c.mu.Unlock()
		return context.Canceled
	case <-done:
		c.mu.Lock()
		c.waiters--
		err := c.err
		if c.closed && err == nil {
			err = context.Canceled
		}
		c.mu.Unlock()
		if err != nil {
			c.stop()
			return errors.Join(err, c.waitStop(true))
		}
		return err
	}
}

func (c *startupClient) start(ctx context.Context) {
	err := c.CopilotClient.Start(ctx)
	c.mu.Lock()
	closed := c.closed
	if closed && err == nil {
		err = context.Canceled
	}
	c.err = err
	if err != nil {
		c.cancel()
	}
	c.finished = true
	close(c.done)
	c.mu.Unlock()
	if closed || err != nil {
		c.stop()
	}
}

// stop launches exactly one cleanup, including for a late successful Start.
func (c *startupClient) stop() {
	c.stopOnce.Do(func() {
		go func() {
			c.mu.Lock()
			pending := c.done != nil && !c.finished
			done := c.done
			c.mu.Unlock()
			if pending {
				if force, ok := c.CopilotClient.(interface{ ForceStop() }); ok {
					force.ForceStop()
				}
				<-done
			}
			c.stopErr = c.CopilotClient.Stop()
			close(c.stopDone)
		}()
	})
}

func (c *startupClient) Stop() error {
	c.mu.Lock()
	bounded := c.done != nil && (!c.finished || c.err != nil)
	c.closeLocked()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.stop()
	return c.waitStop(bounded)
}

func (c *startupClient) cancelPending() {
	c.mu.Lock()
	pending := c.done != nil && !c.finished
	c.closeLocked()
	if pending {
		c.cancel()
	}
	c.mu.Unlock()
	if pending {
		c.stop()
	}
}

// replaceable keeps pending cleanup in the shared registry's ownership. A
// successfully cleaned abandoned client can be replaced without affecting
// healthy owners. Failed cleanup remains available to global shutdown.
func (c *startupClient) replaceable() bool {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		return false
	}
	select {
	case <-c.stopDone:
		return c.stopErr == nil
	default:
		return false
	}
}

func (e *CopilotEngine) cancelPendingStartup() {
	if e.ownsClient {
		if client, ok := e.client.(*startupClient); ok {
			client.cancelPending()
		}
	}
}

func (c *startupClient) waitStop(bounded bool) error {
	if !bounded {
		<-c.stopDone
		return c.stopErr
	}
	timer := time.NewTimer(startupCleanupTimeout)
	defer timer.Stop()
	select {
	case <-c.stopDone:
		return c.stopErr
	case <-timer.C:
		return errors.New("Copilot client startup cleanup exceeded 2s; client must unblock Start on cancellation or ForceStop")
	}
}
