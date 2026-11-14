package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestCoreBootstrapDoesNotWaitForStorkOrPostgres(t *testing.T) {
	var calls []string
	ops := coreOperations{
		wait: func(_ context.Context, name, address string, _ time.Duration) error {
			calls = append(calls, "wait "+name+" "+address)
			return nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			calls = append(calls, "run "+name)
			return nil
		},
		write: func(path string) error {
			calls = append(calls, "write "+path)
			return nil
		},
	}
	err := bootstrapCore(context.Background(), "192.0.2.10", true, time.Second, ops)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"wait BIND 192.0.2.10:5353",
		"wait Samba LDAP 127.0.0.1:389",
		"wait Samba KDC 127.0.0.1:88",
		"run /usr/local/bin/dib-identity-core-ctl",
		"run /usr/local/bin/dib-network-core-ctl",
		"write " + coreMarker,
		"write " + coreReadyFile,
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("core bootstrap calls = %v, want %v", calls, want)
	}
}

func TestCoreBootstrapFailureDoesNotMarkReady(t *testing.T) {
	var writes []string
	ops := coreOperations{
		wait: func(context.Context, string, string, time.Duration) error { return nil },
		command: func(_ context.Context, name string, _ ...string) error {
			if name == "/usr/local/bin/dib-network-core-ctl" {
				return errors.New("network bootstrap failed")
			}
			return nil
		},
		write: func(path string) error {
			writes = append(writes, path)
			return nil
		},
	}
	if err := bootstrapCore(context.Background(), "192.0.2.10", true, time.Second, ops); err == nil {
		t.Fatal("expected network bootstrap failure")
	}
	if len(writes) != 0 {
		t.Fatalf("failed bootstrap wrote readiness or completion markers: %v", writes)
	}
}

func TestStorkRetriesUntilDependenciesRecover(t *testing.T) {
	attempts, reports := 0, 0
	err := retryStork(context.Background(), time.Millisecond, func(context.Context) error {
		attempts++
		if attempts < 3 {
			return errors.New("dependency unavailable")
		}
		return nil
	}, func(error) { reports++ })
	if err != nil || attempts != 3 || reports != 2 {
		t.Fatalf("err=%v attempts=%d reported failures=%d", err, attempts, reports)
	}
}

func TestStorkBootstrapWaitsForCoreAndDoesNotMarkFailedRegistrationComplete(t *testing.T) {
	var calls []string
	ops := storkOperations{
		waitCore: func(context.Context) error {
			calls = append(calls, "wait core")
			return nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			calls = append(calls, "run "+name)
			return errors.New("Stork unavailable")
		},
		write: func(path string) error {
			calls = append(calls, "write "+path)
			return nil
		},
	}
	if err := bootstrapStork(context.Background(), ops); err == nil {
		t.Fatal("expected registration failure")
	}
	want := []string{"wait core", "run /usr/local/bin/dib-observability-ctl"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Stork bootstrap calls = %v, want %v", calls, want)
	}
}

func TestStorkBootstrapDoesNotRegisterBeforeCoreIsReady(t *testing.T) {
	var calls []string
	ops := storkOperations{
		waitCore: func(context.Context) error {
			calls = append(calls, "wait core")
			return errors.New("core not ready")
		},
		command: func(_ context.Context, name string, _ ...string) error {
			calls = append(calls, "run "+name)
			return nil
		},
		write: func(path string) error {
			calls = append(calls, "write "+path)
			return nil
		},
	}
	if err := bootstrapStork(context.Background(), ops); err == nil {
		t.Fatal("expected core readiness failure")
	}
	if !reflect.DeepEqual(calls, []string{"wait core"}) {
		t.Fatalf("Stork registered before core was ready: %v", calls)
	}
}

func TestCoreRestartRefreshesNetworkBeforeMarkingReady(t *testing.T) {
	var calls []string
	ops := coreOperations{
		wait: func(_ context.Context, name, _ string, _ time.Duration) error {
			calls = append(calls, "wait "+name)
			return nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			calls = append(calls, "run "+name)
			return nil
		},
		write: func(path string) error {
			calls = append(calls, "write "+path)
			return nil
		},
	}
	if err := bootstrapCore(context.Background(), "192.0.2.10", false, time.Second, ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"wait Samba LDAP", "wait Samba KDC", "run /usr/local/bin/dib-network-core-ctl", "write " + coreReadyFile}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("core restart calls = %v, want %v", calls, want)
	}
}

func TestStorkBootstrapWritesCompletionOnlyAfterRegistration(t *testing.T) {
	var calls []string
	ops := storkOperations{
		waitCore: func(context.Context) error {
			calls = append(calls, "wait core")
			return nil
		},
		command: func(_ context.Context, name string, _ ...string) error {
			calls = append(calls, "run "+name)
			return nil
		},
		write: func(path string) error {
			calls = append(calls, "write "+path)
			return nil
		},
	}
	if err := bootstrapStork(context.Background(), ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"wait core", "run /usr/local/bin/dib-observability-ctl", "write " + storkMarker}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("Stork bootstrap calls = %v, want %v", calls, want)
	}
}

func TestStorkRetryStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := retryStork(ctx, time.Hour, func(context.Context) error {
		attempts++
		return errors.New("unavailable")
	}, func(error) { cancel() })
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}
