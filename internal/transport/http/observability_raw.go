package http

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Raw (non-Huma) routes.
//
//	POST /otlp/v1/traces  OTLP/HTTP spans; bearer = the environment's ingest key (app.RelayTraces)
//	GET  /worker/config   log sinks + the services each covers, for the per-node agents (bearer
//	                      KEEL_WORKER_TOKEN). Shape unchanged from convex/worker.ts: deployed
//	                      agents and install.sh call it. GET /agent/config is the same route.
func (s *Server) registerObservabilityRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /otlp/v1/traces", s.obsOTLPTraces)
	mux.HandleFunc("GET /worker/config", s.obsWorkerConfig)
	mux.HandleFunc("GET /agent/config", s.obsWorkerConfig)
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

// obsWorkerAuthorized: `Authorization: Bearer <KEEL_WORKER_TOKEN>` (case-sensitive "Bearer ",
// constant-time compare). An unset token rejects everything.
func (s *Server) obsWorkerAuthorized(r *http.Request) bool {
	expected := s.app.Config.WorkerToken
	header := r.Header.Get("Authorization")
	if expected == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}

// obsWorkerConfigBody is GET /worker/config, exactly as convex/worker.ts returned it.
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
