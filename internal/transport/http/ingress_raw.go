package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
)

func (s *Server) registerIngressRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /proxy/events", s.proxyEvents)
}

func (s *Server) proxyEvents(w http.ResponseWriter, r *http.Request) {
	if !ingressBearerOK(r, s.app.Config.WorkerToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil || report.Name == "" || (report.Event != app.CertObtained && report.Event != app.CertFailed) {
		http.Error(w, "bad report", http.StatusBadRequest)
		return
	}
	if err := s.app.ReportCert(r.Context(), report.Event, report.Name, report.Error); err != nil {
		s.app.Log.Error("proxy cert report", "name", report.Name, "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(w, "ok")
}

func ingressBearerOK(r *http.Request, expected string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || !ok {
		return false
	}
	a, b := sha256.Sum256([]byte(strings.TrimSpace(got))), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
