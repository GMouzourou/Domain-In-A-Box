package probe

import (
	"fmt"
	"net"
	"time"
)

func TCP(address string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return fmt.Errorf("tcp probe failed for %s: %w", address, err)
	}
	_ = conn.Close()
	return nil
}

func UDP(address string, timeout time.Duration) error {
	conn, err := net.DialTimeout("udp", address, timeout)
	if err != nil {
		return fmt.Errorf("udp probe failed for %s: %w", address, err)
	}
	_ = conn.Close()
	return nil
}
