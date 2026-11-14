package identitycore

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSecondaryJoinArguments(t *testing.T) {
	args := secondaryJoinArguments("DOMAIN.HOME.ARPA", "192.0.2.10", "192.0.2.11", "net1", "on")
	want := []string{
		"domain", "join", "DOMAIN.HOME.ARPA", "DC",
		"--server=192.0.2.10",
		"--dns-backend=SAMBA_INTERNAL",
		"--option=bind interfaces only = yes",
		"--option=interfaces = lo net1",
		"--option=dns forwarder = 192.0.2.11:5353",
		"--option=rpc server dynamic port range = 49152-49252",
		"--option=idmap_ldb:use rfc2307 = yes",
		"--option=ad dc functional level = 2016",
		"--option=smbd profiling level = on",
		"-U", "Administrator",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("secondaryJoinArguments() = %#v, want %#v", args, want)
	}
	for _, arg := range args {
		if strings.Contains(arg, "password") || strings.Contains(arg, "secret") {
			t.Fatalf("credentials must not appear in command arguments: %q", arg)
		}
	}
}

func TestRunWithInput(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "read-stdin")
	output := filepath.Join(directory, "stdin.txt")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := runWithInput(context.Background(), "secret-value\n", script, output); err != nil {
		t.Fatalf("runWithInput() error = %v", err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "secret-value\n" {
		t.Fatalf("stdin = %q, want secret on stdin", contents)
	}
}

func TestSetSambaParameter(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents string
		want     string
		wantErr  bool
	}{
		{
			name:     "insert",
			contents: "[global]\n\tworkgroup = DOMAIN\n\n[sysvol]\n",
			want:     "[global]\nad dc functional level = 2016\n\tworkgroup = DOMAIN\n\n[sysvol]\n",
		},
		{
			name:     "replace",
			contents: "[global]\nad dc functional level = 2008_R2\n",
			want:     "[global]\nad dc functional level = 2016\n",
		},
		{
			name:     "duplicate",
			contents: "[global]\nad dc functional level = 2008_R2\nad dc functional level = 2016\n",
			wantErr:  true,
		},
		{
			name:     "no global section",
			contents: "[sysvol]\n",
			wantErr:  true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "smb.conf")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			err := setSambaParameter(path, "ad dc functional level", domainFunctionalLevel)
			if (err != nil) != test.wantErr {
				t.Fatalf("setSambaParameter() error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want {
				t.Fatalf("smb.conf = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRewriteSambaShareReadOnly(t *testing.T) {
	config := []byte(`[global]
	read only = no

[sysvol]
	path = /var/lib/samba/sysvol
	writeable = yes
	read only = no

[netlogon]
	path = /var/lib/samba/sysvol/domain.home.arpa/scripts
`)

	got, err := rewriteSambaShareReadOnly(config, "sysvol")
	if err != nil {
		t.Fatal(err)
	}
	updated := string(got)
	if !strings.Contains(updated, "[sysvol]\n\tpath = /var/lib/samba/sysvol\n\tread only = yes\n\n[netlogon]") {
		t.Fatalf("SYSVOL share was not made read-only: %s", updated)
	}
	if !strings.Contains(updated, "[global]\n\tread only = no") {
		t.Fatalf("global Samba settings changed: %s", updated)
	}
	if strings.Contains(updated, "writeable = yes") ||
		strings.Contains(updated, "[sysvol]\n\tpath = /var/lib/samba/sysvol\n\tread only = no") {
		t.Fatalf("writable SYSVOL directives remain: %s", updated)
	}

	got, err = rewriteSambaShareReadOnly(got, "netlogon")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[netlogon]\n\tpath = /var/lib/samba/sysvol/domain.home.arpa/scripts\nread only = yes\n") {
		t.Fatalf("NETLOGON share was not made read-only: %s", got)
	}
}

func TestRewriteSambaShareReadOnlyRejectsMissingOrDuplicateShares(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  string
		wantErr bool
	}{
		{name: "missing", config: "[global]\n", wantErr: true},
		{name: "duplicate", config: "[sysvol]\npath = /a\n[sysvol]\npath = /b\n", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := rewriteSambaShareReadOnly([]byte(test.config), "sysvol")
			if (err != nil) != test.wantErr {
				t.Fatalf("rewriteSambaShareReadOnly() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
