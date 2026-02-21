package helpers

import (
	"fmt"
	"time"
)

// RetryConfig holds retry configuration
type RetryConfig struct {
	MaxAttempts int
	Delay       time.Duration
	Verbose     bool
}

// DefaultRetryConfig returns a default retry configuration
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: 5,
		Delay:       2 * time.Second,
		Verbose:     false,
	}
}

// Retry runs a function with exponential backoff retries
func Retry(desc string, fn func() error, cfg RetryConfig) error {
	var lastErr error

	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if attempt < cfg.MaxAttempts {
			if cfg.Verbose {
				LogTest(fmt.Sprintf("Attempt %d failed. Retrying in %v...", attempt, cfg.Delay))
			}
			time.Sleep(cfg.Delay)
		}
	}

	return fmt.Errorf("command failed after %d attempts: %w", cfg.MaxAttempts, lastErr)
}
