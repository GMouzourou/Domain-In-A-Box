package sysvolsync

import "testing"

func TestValidIPAcceptsIPv4Only(t *testing.T) {
	for _, test := range []struct {
		value string
		valid bool
	}{
		{value: "192.0.2.10", valid: true},
		{value: "2001:db8::10", valid: false},
		{value: "not-an-ip", valid: false},
	} {
		if got := validIP(test.value); got != test.valid {
			t.Errorf("validIP(%q) = %t, want %t", test.value, got, test.valid)
		}
	}
}
