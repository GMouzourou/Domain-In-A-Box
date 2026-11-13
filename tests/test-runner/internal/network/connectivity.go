package network

import (
	"context"
	"fmt"
	"net"
	"time"
)

// CheckPort checks if a port is open on a host
func CheckPort(host string, port string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return fmt.Errorf("port check failed: %w", err)
	}
	defer conn.Close()
	return nil
}
