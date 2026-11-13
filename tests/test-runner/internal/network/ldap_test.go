package network

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLDAPTLSConfigVerifiesCertificatesByDefault(t *testing.T) {
	config, err := ldapTLSConfig("ldap.example.test", "", false)
	if err != nil {
		t.Fatalf("ldapTLSConfig() error = %v", err)
	}
	if config.InsecureSkipVerify {
		t.Fatal("ldapTLSConfig() skipped certificate verification by default")
	}
	if config.ServerName != "ldap.example.test" {
		t.Fatalf("LDAP TLS server name = %q, want ldap.example.test", config.ServerName)
	}
}

func TestLDAPTLSConfigAllowsExplicitInsecureTestOptIn(t *testing.T) {
	config, err := ldapTLSConfig("ldap.example.test", "", true)
	if err != nil {
		t.Fatalf("ldapTLSConfig() error = %v", err)
	}
	if !config.InsecureSkipVerify {
		t.Fatal("ldapTLSConfig() did not honor the explicit insecure test opt-in")
	}
}

func TestLDAPTLSConfigRejectsInvalidCAFile(t *testing.T) {
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ldapTLSConfig("ldap.example.test", caPath, false); err == nil {
		t.Fatal("ldapTLSConfig() accepted a CA file without a valid certificate")
	}
}

func TestLDAPTLSConfigLoadsCustomCA(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	certificate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := ldapTLSConfig("192.0.2.1", caPath, false)
	if err != nil {
		t.Fatalf("ldapTLSConfig() error = %v", err)
	}
	if config.InsecureSkipVerify || config.RootCAs == nil || config.ServerName != "192.0.2.1" {
		t.Fatalf("LDAP TLS config = %+v, want custom roots and verified server name", config)
	}
}
