package identitycore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
)

func (r *Runner) RunLDAPSRelay(ctx context.Context) error {
	address := os.Getenv("DIB_LDAPS_RELAY_ADDRESS")
	if address == "" {
		r.log.Infof("LDAPS relay is disabled")
		return nil
	}
	host, err := ldapsRelayAddress(address, os.Getenv("IP"))
	if err != nil {
		return err
	}
	if host == "" {
		r.log.Infof("LDAPS relay is unnecessary: Samba already binds the pod address")
		return nil
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, "636"))
	if err != nil {
		return fmt.Errorf("listen for internal LDAPS: %w", err)
	}
	r.log.Infof("Relaying LDAPS on %s to 127.0.0.1:636 without terminating TLS", listener.Addr())
	return serveLDAPSRelay(ctx, listener, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", "127.0.0.1:636")
	}, r.log)
}

func ldapsRelayAddress(address, lanAddress string) (string, error) {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.IsGlobalUnicast() || ip.IsLoopback() {
		return "", fmt.Errorf("DIB_LDAPS_RELAY_ADDRESS must be a unicast pod IP, got %q", address)
	}
	lanIP, err := netip.ParseAddr(lanAddress)
	if err != nil {
		return "", fmt.Errorf("IP must identify the controller LAN address, got %q", lanAddress)
	}
	if ip.Unmap() == lanIP.Unmap() {
		return "", nil
	}
	return ip.String(), nil
}

func serveLDAPSRelay(ctx context.Context, listener net.Listener, dial func(context.Context) (net.Conn, error), logger *platformlog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	var active sync.WaitGroup
	slots := make(chan struct{}, 64)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer func() {
		cancel()
		listener.Close()
		stop()
		active.Wait()
	}()
	for {
		client, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept internal LDAPS connection: %w", err)
		}
		select {
		case slots <- struct{}{}:
			active.Add(1)
			go func() {
				defer active.Done()
				defer func() { <-slots }()
				relayLDAPSConnection(ctx, client, dial, logger)
			}()
		default:
			logger.Errorf("LDAPS relay connection limit reached; rejecting connection")
			client.Close()
		}
	}
}

func relayLDAPSConnection(ctx context.Context, client net.Conn, dial func(context.Context) (net.Conn, error), logger *platformlog.Logger) {
	defer client.Close()
	stopClient := context.AfterFunc(ctx, func() { client.Close() })
	defer stopClient()
	upstream, err := dial(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Errorf("connect LDAPS relay to Samba: %v", err)
		}
		return
	}
	defer upstream.Close()
	stopUpstream := context.AfterFunc(ctx, func() { upstream.Close() })
	defer stopUpstream()
	copyDirection := func(destination, source net.Conn) {
		if _, err := io.Copy(destination, source); err != nil && ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
			logger.Errorf("copy LDAPS relay stream: %v", err)
		}
		if tcp, ok := destination.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		copyDirection(upstream, client)
	}()
	copyDirection(client, upstream)
	<-done
}
