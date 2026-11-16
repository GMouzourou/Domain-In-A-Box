package dnsbridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
)

type Runner struct {
	ConfigPath string
	Liveness   bool
	log        *platformlog.Logger
}

func NewRunner() *Runner {
	return &Runner{ConfigPath: os.Getenv("DIB_DNS_BRIDGE_CONFIG"), log: platformlog.New("dns-bridge")}
}

func (r *Runner) Name() string { return "dns-bridge" }

func (r *Runner) Configure(ctx context.Context) error { return r.Validate(ctx) }

func (r *Runner) Bootstrap(context.Context) error {
	return errors.New("use the explicit provision command inside a domain controller")
}

func (r *Runner) Validate(context.Context) error {
	cfg, err := LoadConfig(r.ConfigPath)
	if err != nil {
		return err
	}
	policy, err := NewPolicy(cfg)
	if err != nil {
		return err
	}
	if _, err := NewBackend(cfg, policy); err != nil {
		return err
	}
	for _, binary := range []string{"kinit", "nsupdate"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("required bridge executable %s: %w", binary, err)
		}
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	if err := r.Validate(ctx); err != nil {
		return err
	}
	cfg, err := LoadConfig(r.ConfigPath)
	if err != nil {
		return err
	}
	policy, err := NewPolicy(cfg)
	if err != nil {
		return err
	}
	backend, err := NewBackend(cfg, policy)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: cfg.Listen, Handler: NewServer(policy, backend, r.log).Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 65 * time.Second, IdleTimeout: 60 * time.Second,
	}
	finished := make(chan error, 1)
	go func() { finished <- server.ListenAndServe() }()
	r.log.Infof("Starting pod-local webhook for zone %s on %s", cfg.Zone, cfg.Listen)
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return err
		}
		if err := <-finished; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func (r *Runner) Health(ctx context.Context) error {
	cfg, err := LoadConfig(r.ConfigPath)
	if err != nil {
		return err
	}
	path := "/healthz"
	if r.Liveness {
		path = "/"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.Listen+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", MediaType)
	client := &http.Client{Timeout: 25 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("bridge health request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge health returned HTTP %d", response.StatusCode)
	}
	return nil
}
