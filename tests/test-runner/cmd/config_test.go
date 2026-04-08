package cmd

import "testing"

func TestEnvOrDefaultUsesFirstNonEmptyValue(t *testing.T) {
	t.Setenv("EMPTY_ENV", "   ")
	t.Setenv("REAL_VALUE", "home.arpa")

	got := envOrDefault("fallback", "EMPTY_ENV", "REAL_VALUE")
	if got != "home.arpa" {
		t.Fatalf("envOrDefault() = %q, want %q", got, "home.arpa")
	}
}

func TestEnvOrDefaultFallsBackWhenUnset(t *testing.T) {
	got := envOrDefault("fallback", "MISSING_ONE", "MISSING_TWO")
	if got != "fallback" {
		t.Fatalf("envOrDefault() = %q, want %q", got, "fallback")
	}
}

func TestServicePortUsesMappedPortForLocalhost(t *testing.T) {
	got := servicePort("127.0.0.1", "389", "3389")
	if got != "3389" {
		t.Fatalf("servicePort() = %q, want %q", got, "3389")
	}
}

func TestServicePortUsesDefaultPortForRemoteHost(t *testing.T) {
	got := servicePort("192.168.1.1", "389", "3389")
	if got != "389" {
		t.Fatalf("servicePort() = %q, want %q", got, "389")
	}
}
