package network

import (
	"context"
	"fmt"
	"time"

	"github.com/miekg/dns"
)

// DNSClient wraps DNS operations
type DNSClient struct {
	Server  string
	Timeout time.Duration
}

// NewDNSClient creates a new DNS client
func NewDNSClient(server string) *DNSClient {
	port := "53"
	if server == "localhost" || server == "127.0.0.1" {
		port = "5353" // Mapped DNS port
	}
	return &DNSClient{
		Server:  server + ":" + port,
		Timeout: 5 * time.Second,
	}
}

// LookupA resolves an A record
func (d *DNSClient) LookupA(domain string) (string, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(domain), dns.TypeA)

	r, err := dns.ExchangeContext(context.Background(), m, d.Server)
	if err != nil {
		return "", fmt.Errorf("DNS query failed: %w", err)
	}

	if len(r.Answer) == 0 {
		return "", fmt.Errorf("no A record found for %s", domain)
	}

	for _, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok {
			return a.A.String(), nil
		}
	}

	return "", fmt.Errorf("no A record found for %s", domain)
}

// LookupSRV resolves a SRV record
func (d *DNSClient) LookupSRV(service, proto, name string) ([]*dns.SRV, error) {
	m := new(dns.Msg)
	srvName := fmt.Sprintf("_%s._%s.%s", service, proto, dns.Fqdn(name))
	m.SetQuestion(srvName, dns.TypeSRV)

	r, err := dns.ExchangeContext(context.Background(), m, d.Server)
	if err != nil {
		return nil, fmt.Errorf("SRV query failed: %w", err)
	}

	if len(r.Answer) == 0 {
		return nil, fmt.Errorf("no SRV record found for %s", srvName)
	}

	var srvs []*dns.SRV
	for _, ans := range r.Answer {
		if srv, ok := ans.(*dns.SRV); ok {
			srvs = append(srvs, srv)
		}
	}

	if len(srvs) == 0 {
		return nil, fmt.Errorf("no SRV records found")
	}

	return srvs, nil
}

// LookupPTR resolves a reverse DNS lookup
func (d *DNSClient) LookupPTR(ip string) (string, error) {
	m := new(dns.Msg)
	ptr, err := dns.ReverseAddr(ip)
	if err != nil {
		return "", fmt.Errorf("reverse addr failed: %w", err)
	}

	m.SetQuestion(ptr, dns.TypePTR)

	r, err := dns.ExchangeContext(context.Background(), m, d.Server)
	if err != nil {
		return "", fmt.Errorf("PTR query failed: %w", err)
	}

	if len(r.Answer) == 0 {
		return "", fmt.Errorf("no PTR record found for %s", ip)
	}

	for _, ans := range r.Answer {
		if p, ok := ans.(*dns.PTR); ok {
			return p.Ptr, nil
		}
	}

	return "", fmt.Errorf("no PTR record found")
}
