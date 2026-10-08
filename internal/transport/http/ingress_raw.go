package http

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ThallesP/keel/internal/app"
)

// Raw (non-Huma) routes. Owner: the ingress area.
//
//	POST /proxy/events  keel-proxy's `keel` event handler reporting a certificate obtained or
//	                    failed. Same bearer as the agent (KEEL_WORKER_TOKEN), which the sync writes
//	                    into the proxy's config. Path and answers unchanged from Convex
//	                    (proxy-ingress.md §5.9): deployed proxies keep calling it.
func (s *Server) registerIngressRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /proxy/events", s.proxyEvents)
}

// ingressMaxBody: 256 KiB counted as JavaScript string length (UTF-16 code units), as Convex did.
const ingressMaxBody = 256 * 1024

func (s *Server) proxyEvents(w http.ResponseWriter, r *http.Request) {
	if !ingressBearerOK(r, s.app.Config.WorkerToken) {
		ingressText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// A UTF-16 unit takes at most 3 UTF-8 bytes: more than 3×max bytes is more than max units.
	body, err := io.ReadAll(io.LimitReader(r.Body, 3*ingressMaxBody+1))
	if err != nil {
		ingressText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > 3*ingressMaxBody || ingressUTF16Len(body) > ingressMaxBody {
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
		s.log.Error("proxy cert report", "name", name, "err", err)
		ingressText(w, http.StatusInternalServerError, "server error")
		return
	}
	ingressText(w, http.StatusOK, "ok")
}

// ingressBearerOK: `Authorization: Bearer <token>` with the trimmed token equal to expected, in
// constant time (a length mismatch counts as a difference, not a shortcut). No expected token →
// never.
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

// ingressUTF16Len is the JavaScript length of the UTF-8 text b (invalid bytes count one each, as the
// decoder's replacement characters would).
func ingressUTF16Len(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func ingressText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain;charset=UTF-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
