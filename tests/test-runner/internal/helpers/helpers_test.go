package helpers

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConvertDomainToLDAP(t *testing.T) {
	got := ConvertDomainToLDAP("home.arpa")
	want := "dc=home,dc=arpa"
	if got != want {
		t.Fatalf("ConvertDomainToLDAP() = %q, want %q", got, want)
	}
}

func TestRetryEventuallySucceeds(t *testing.T) {
	attempts := 0
	err := Retry("eventual success", func() error {
		attempts++
		if attempts < 3 {
			return errors.New("not yet")
		}
		return nil
	}, RetryConfig{MaxAttempts: 3, Delay: time.Millisecond})
	if err != nil {
		t.Fatalf("Retry() returned unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("Retry() attempts = %d, want %d", attempts, 3)
	}
}

func TestRetryReturnsLastErrorAfterMaxAttempts(t *testing.T) {
	attempts := 0
	err := Retry("always fails", func() error {
		attempts++
		return errors.New("boom")
	}, RetryConfig{MaxAttempts: 3, Delay: time.Millisecond})
	if err == nil {
		t.Fatal("Retry() error = nil, want non-nil")
	}
	if attempts != 3 {
		t.Fatalf("Retry() attempts = %d, want %d", attempts, 3)
	}
	if !strings.Contains(err.Error(), "command failed after 3 attempts") {
		t.Fatalf("Retry() error = %q, want max-attempt message", err.Error())
	}
}
