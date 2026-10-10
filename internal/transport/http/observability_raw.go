package http

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func (s *Server) registerObservabilityRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /otlp/v1/traces", s.obsOTLPTraces)
	mux.HandleFunc("GET /worker/config", s.obsWorkerConfig)
}

func (s *Server) obsOTLPTraces(w http.ResponseWriter, r *http.Request) {
	res := s.app.RelayTraces(r.Context(), app.OTLPRequest{
		Authorization:   r.Header.Get("Authorization"),
		ContentType:     r.Header.Get("Content-Type"),
		ContentLength:   r.ContentLength,
		ContentEncoding: r.Header.Get("Content-Encoding"),
		Body:            r.Body,
	})
	ct := res.ContentType
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (s *Server) obsWorkerAuthorized(r *http.Request) bool {
	expected := s.app.Config.WorkerToken
	header := r.Header.Get("Authorization")
	if expected == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

type obsWorkerConfigBody struct {
	Sinks []obsWorkerConfigSink `json:"sinks"`
}

type obsWorkerConfigSink struct {
	ProjectID  string         `json:"projectId"`
	ServiceIDs []string       `json:"serviceIds"`
	Sink       domain.LogSink `json:"sink"`
	Since      int64          `json:"since"`
}

func (s *Server) obsWorkerConfig(w http.ResponseWriter, r *http.Request) {
	if !s.obsWorkerAuthorized(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
		return
	}
	entries, err := s.app.WorkerConfig(r.Context())
	if err != nil {
		s.app.Log.Error("worker config", "err", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
		return
	}
	body := obsWorkerConfigBody{Sinks: make([]obsWorkerConfigSink, len(entries))}
	for i, e := range entries {
		body.Sinks[i] = obsWorkerConfigSink{ProjectID: e.ProjectID, ServiceIDs: e.ServiceIDs, Sink: e.Sink, Since: e.Since}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}
