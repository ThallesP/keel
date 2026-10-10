package http

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func (s *Server) registerIngressRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /proxy/events", s.proxyEvents)
}

const ingressMaxBody = 256 * 1024

func (s *Server) proxyEvents(w http.ResponseWriter, r *http.Request) {
	if !ingressBearerOK(r, s.app.Config.WorkerToken) {
		ingressText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 3*ingressMaxBody+1))
	if err != nil {
		ingressText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > 3*ingressMaxBody || domain.UTF16Len(string(body)) > ingressMaxBody {
		ingressText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		ingressText(w, http.StatusBadRequest, "bad json")
		return
	}
	report, _ := v.(map[string]any)
	event, _ := report["event"].(string)
	name, isString := report["name"].(string)
	if (event != app.CertObtained && event != app.CertFailed) || !isString {
		ingressText(w, http.StatusBadRequest, "bad report")
		return
	}
	certError, _ := report["error"].(string)
	if err := s.app.ReportCert(r.Context(), event, name, certError); err != nil {
		s.app.Log.Error("proxy cert report", "name", name, "err", err)
		ingressText(w, http.StatusInternalServerError, "server error")
		return
	}
	ingressText(w, http.StatusOK, "ok")
}

func ingressBearerOK(r *http.Request, expected string) bool {
	header := r.Header.Get("Authorization")
	if expected == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	lengthOK := subtle.ConstantTimeEq(int32(len(got)), int32(len(expected)))
	n := max(len(got), len(expected))
	a, b := make([]byte, n), make([]byte, n)
	copy(a, got)
	copy(b, expected)
	return subtle.ConstantTimeCompare(a, b)&lengthOK == 1
}

func ingressText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
