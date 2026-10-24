package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRunOnceRunsActionOnlyOnce(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "complete")
	runs := 0
	action := func(context.Context) error {
		runs++
		return nil
	}

	if err := RunOnce(context.Background(), marker, action); err != nil {
		t.Fatal(err)
	}
	if err := RunOnce(context.Background(), marker, action); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("action ran %d times, want 1", runs)
	}
}

func TestRunOnceDoesNotMarkFailedActionComplete(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "complete")
	wantErr := errors.New("bootstrap failed")
	if err := RunOnce(context.Background(), marker, func(context.Context) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("RunOnce error = %v, want %v", err, wantErr)
	}
	if err := RunOnce(context.Background(), marker, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("retry after failed action: %v", err)
	}
}
