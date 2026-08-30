package wait

import (
	"context"
	"fmt"
	"time"

	"github.com/GMouzourou/domain-in-a-box/internal/platform/retry"
)

func Until(ctx context.Context, name string, attempts int, delay time.Duration, condition func() error) error {
	err := retry.Do(ctx, attempts, delay, condition)
	if err != nil {
		return fmt.Errorf("wait for %s failed: %w", name, err)
	}
	return nil
}
