package dnsbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	platformlog "github.com/GMouzourou/domain-in-a-box/internal/platform/log"
)

const MediaType = "application/external.dns.webhook+json;version=1"

type Server struct {
	policy  *Policy
	backend Backend
	log     *platformlog.Logger
	writeMu sync.Mutex
}

func NewServer(policy *Policy, backend Backend, logger *platformlog.Logger) *Server {
	return &Server{policy: policy, backend: backend, log: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, req *http.Request) {
		accept := req.Header.Get("Accept")
		if accept != "" && accept != "*/*" && accept != MediaType {
			s.fail(w, http.StatusNotAcceptable, errors.New("unsupported webhook protocol version"))
			return
		}
		s.respond(w, map[string][]string{"filters": {s.policy.cfg.Zone}})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, req *http.Request) {
		if _, err := s.snapshot(req); err != nil {
			s.fail(w, http.StatusServiceUnavailable, err)
			return
		}
		s.respond(w, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /records", s.records)
	mux.HandleFunc("POST /records", s.apply)
	mux.HandleFunc("POST /adjustendpoints", s.adjust)
	return mux
}

func (s *Server) snapshot(req *http.Request) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	snapshot, err := s.backend.Records(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Records == nil || snapshot.Names == nil || snapshot.Foreign == nil {
		return Snapshot{}, errors.New("backend returned an incomplete DNS snapshot")
	}
	return snapshot, nil
}

func (s *Server) records(w http.ResponseWriter, req *http.Request) {
	snapshot, err := s.snapshot(req)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, err)
		return
	}
	records := make([]Endpoint, 0, len(snapshot.Records))
	for _, record := range snapshot.Records {
		records = append(records, record)
	}
	slices.SortFunc(records, func(a, b Endpoint) int {
		if order := strings.Compare(a.DNSName, b.DNSName); order != 0 {
			return order
		}
		return strings.Compare(a.RecordType, b.RecordType)
	})
	s.respond(w, records)
}

func (s *Server) adjust(w http.ResponseWriter, req *http.Request) {
	var endpoints []Endpoint
	if !s.decode(w, req, &endpoints) {
		return
	}
	if len(endpoints) > 1000 {
		s.fail(w, http.StatusBadRequest, errors.New("too many desired endpoints"))
		return
	}
	normalized := make([]Endpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		record, err := s.policy.normalize(endpoint, true)
		if errors.Is(err, errProtectedName) {
			s.log.Warnf("Ignoring desired endpoint %s: application name is protected", normalizeName(endpoint.DNSName))
			continue
		}
		if err != nil {
			s.fail(w, http.StatusBadRequest, fmt.Errorf("invalid desired endpoint: %w", err))
			return
		}
		normalized = append(normalized, record)
	}
	snapshot, err := s.snapshot(req)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, err)
		return
	}
	eligible := make([]Endpoint, 0, len(normalized))
	for _, record := range normalized {
		application, err := s.policy.applicationName(record.DNSName, record.RecordType)
		if err != nil {
			s.fail(w, http.StatusBadRequest, err)
			return
		}
		if s.policy.applicationConflict(snapshot, application) {
			s.log.Warnf("Ignoring desired endpoint %s: existing DNS name is unowned or conflicting; explicit migration is required", application)
			continue
		}
		eligible = append(eligible, record)
	}
	s.respond(w, eligible)
}

func (s *Server) apply(w http.ResponseWriter, req *http.Request) {
	var changes *Changes
	if !s.decode(w, req, &changes) {
		return
	}
	if changes == nil {
		s.fail(w, http.StatusBadRequest, errors.New("changes must be a JSON object, not null"))
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	snapshot, err := s.snapshot(req)
	if err != nil {
		s.fail(w, http.StatusServiceUnavailable, err)
		return
	}
	mutation, err := s.policy.plan(snapshot, *changes)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrConflict) {
			status = http.StatusConflict
		}
		s.fail(w, status, err)
		return
	}
	if !mutation.empty() {
		ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
		defer cancel()
		if err := s.backend.Apply(ctx, snapshot, mutation); err != nil {
			s.fail(w, http.StatusServiceUnavailable, err)
			return
		}
		s.log.Infof("Applied an ownership-checked DNS transaction affecting %d application names", len(mutation.Names)/3)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) decode(w http.ResponseWriter, req *http.Request, destination any) bool {
	req.Body = http.MaxBytesReader(w, req.Body, 1024*1024)
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(destination)
	if err == nil {
		if trailing := decoder.Decode(new(any)); trailing == nil {
			err = errors.New("request must contain exactly one JSON value")
		} else if trailing != io.EOF {
			err = trailing
		}
	}
	if err != nil {
		status := http.StatusBadRequest
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			status = http.StatusRequestEntityTooLarge
		}
		s.fail(w, status, fmt.Errorf("invalid webhook request: %w", err))
		return false
	}
	return true
}

func (s *Server) respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", MediaType)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		s.log.Errorf("Write webhook response: %v", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	s.log.Errorf("Webhook request rejected (HTTP %d): %v", status, err)
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(status)
	if encodingErr := json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}); encodingErr != nil {
		s.log.Errorf("Write webhook error response: %v", encodingErr)
	}
}
