package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ThallesP/keel/internal/app"
)

func (s *Server) registerIngressRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /proxy/events", s.proxyEvents)
}

func (s *Server) proxyEvents(w http.ResponseWriter, r *http.Request) {
	if !s.workerAuthorized(r) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var report struct {
		Event string `json:"event"`
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&report)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	if err != nil || report.Name == "" || (report.Event != app.CertObtained && report.Event != app.CertFailed) {
		writeText(w, http.StatusBadRequest, "bad report")
		return
	}
	if err := s.app.ReportCert(r.Context(), report.Event, report.Name, report.Error); err != nil {
		s.app.Log.Error("proxy cert report", "name", report.Name, "err", err)
		writeText(w, http.StatusInternalServerError, "server error")
		return
	}
	writeText(w, http.StatusOK, "ok")
}
