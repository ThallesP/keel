package http

import (
	"cmp"
	"net/http"

	"github.com/ThallesP/keel/internal/app"
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
	w.Header().Set("Content-Type", cmp.Or(res.ContentType, "text/plain; charset=utf-8"))
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

func (s *Server) obsWorkerConfig(w http.ResponseWriter, r *http.Request) {
	if !s.workerAuthorized(r) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	sinks, err := s.app.WorkerConfig(r.Context())
	if err != nil {
		s.app.Log.Error("worker config", "err", err)
		writeText(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = jsonEncode(w, struct {
		Sinks []app.WorkerSink `json:"sinks"`
	}{sinks})
}
