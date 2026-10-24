package bootstrap

import (
	"context"
	"fmt"
	"os"
)

func RunOnce(ctx context.Context, marker string, action func(context.Context) error) error {
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check bootstrap marker %s: %w", marker, err)
	}

	if err := action(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		return fmt.Errorf("write bootstrap marker %s: %w", marker, err)
	}
	return nil
}
