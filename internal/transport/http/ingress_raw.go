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
		ingressText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		ingressText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	var report struct {
		Event string `json:"event"`
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	if err != nil || json.Unmarshal(body, &report) != nil || report.Name == "" ||
		(report.Event != app.CertObtained && report.Event != app.CertFailed) {
		ingressText(w, http.StatusBadRequest, "bad report")
		return
	}
	if err := s.app.ReportCert(r.Context(), report.Event, report.Name, report.Error); err != nil {
		s.app.Log.Error("proxy cert report", "name", report.Name, "err", err)
		ingressText(w, http.StatusInternalServerError, "server error")
		return
	}
	ingressText(w, http.StatusOK, "ok")
}

func ingressBearerOK(r *http.Request, expected string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || !ok {
		return false
	}
	a, b := sha256.Sum256([]byte(strings.TrimSpace(got))), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func ingressText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
