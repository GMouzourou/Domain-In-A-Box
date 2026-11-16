package identitycore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
)

func TestLDAPSRelayRejectsNonPodBindings(t *testing.T) {
	for _, address := range []string{"", "0.0.0.0", "::", "127.0.0.1", "224.0.0.1", "pod.example.test"} {
		if _, err := ldapsRelayAddress(address, "192.0.2.8"); err == nil {
			t.Errorf("accepted unsafe relay address %q", address)
		}
	}
	if address, err := ldapsRelayAddress("10.244.0.2", "192.0.2.8"); err != nil || address != "10.244.0.2" {
		t.Fatalf("pod binding = %q, %v", address, err)
	}
	for _, address := range []string{"192.0.2.8", "::ffff:192.0.2.8"} {
		if host, err := ldapsRelayAddress(address, "192.0.2.8"); err != nil || host != "" {
			t.Fatalf("relay would bind the LAN interface: %q, %v", host, err)
		}
	}
}

func startTestRelay(t *testing.T, upstream string) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveLDAPSRelay(ctx, listener, func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", upstream)
		}, platformlog.New("test-relay").WithWriter(io.Discard))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("relay failed to shut down")
		}
	})
	return listener
}

func TestLDAPSRelayPreservesEndToEndTLS(t *testing.T) {
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("through Samba TLS"))
	}))
	defer backend.Close()
	relay := startTestRelay(t, backend.Listener.Addr().String())
	roots := x509.NewCertPool()
	roots.AddCert(backend.Certificate())
	client, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", relay.Addr().String(), &tls.Config{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if !client.ConnectionState().PeerCertificates[0].Equal(backend.Certificate()) {
		t.Fatal("relay replaced the upstream TLS certificate")
	}
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(client, "GET / HTTP/1.0\r\nHost: localhost\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response), "through Samba TLS") {
		t.Fatalf("unexpected TLS response: %q", response)
	}
}

func TestLDAPSRelayPreservesTCPHalfClose(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	go func() {
		connection, err := backend.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		connection.SetDeadline(time.Now().Add(5 * time.Second))
		request, err := io.ReadAll(connection)
		if err == nil {
			connection.Write(request)
		}
	}()
	relay := startTestRelay(t, backend.Addr().String())
	client, err := net.DialTCP("tcp", nil, relay.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(client, "request before half-close"); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(client)
	if err != nil || string(response) != "request before half-close" {
		t.Fatalf("response after half-close = %q, %v", response, err)
	}
}
