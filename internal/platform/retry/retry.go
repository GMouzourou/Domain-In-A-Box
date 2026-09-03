package retry

import (
	"context"
	"fmt"
	"time"
)

func Do(ctx context.Context, attempts int, delay time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	if delay < 0 {
		delay = 0
	}

	var lastErr error
	for i := 1; i <= attempts; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if i == attempts {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("retry canceled: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", attempts, lastErr)
}
