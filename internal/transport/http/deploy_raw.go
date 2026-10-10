package http

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func (s *Server) registerDeployRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /worker/events", s.workerEvents)
}

const workerEventsMaxBody = 256 * 1024

func (s *Server) workerEvents(w http.ResponseWriter, r *http.Request) {
	if !deployWorkerAuthorized(r, s.app.Config.WorkerToken) {
		deployWriteText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 3*workerEventsMaxBody+1))
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > 3*workerEventsMaxBody || domain.UTF16Len(string(body)) > workerEventsMaxBody {
		deployWriteText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	events, err := parseWorkerEvents(string(body))
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	s.app.IngestWorkerEvents(r.Context(), events, r.Header.Get("X-Keel-Resync") == "1")
	deployWriteText(w, http.StatusOK, "ok")
}

func deployWorkerAuthorized(r *http.Request, expected string) bool {
	header := r.Header.Get("Authorization")
	if expected == "" || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	got := domain.TrimJS(strings.TrimPrefix(header, "Bearer "))
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func deployWriteText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

var errDeployNotDockerEvent = errors.New("not a Docker event")

func parseWorkerEvents(text string) ([]app.DockerEvent, error) {
	var list []any
	if strings.HasPrefix(domain.TrimJS(text), "[") {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return nil, err
		}
		if arr, ok := v.([]any); ok {
			list = arr
		} else {
			list = []any{v}
		}
	} else {
		for _, line := range strings.Split(text, "\n") {
			if domain.TrimJS(line) == "" {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	}
	events := make([]app.DockerEvent, 0, len(list))
	for _, item := range list {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, errDeployNotDockerEvent
		}
		typ, hasType := e["Type"]
		action, hasAction := e["Action"]
		if !hasType || !hasAction {
			return nil, errDeployNotDockerEvent
		}
		ev := app.DockerEvent{Type: deployJSString(typ), Action: deployJSString(action)}
		if actor, ok := e["Actor"].(map[string]any); ok {
			if attrs, ok := actor["Attributes"].(map[string]any); ok {
				ev.Name, _ = attrs["name"].(string)
				ev.ServiceName, _ = attrs["com.docker.swarm.service.name"].(string)
			}
		}
		if t, ok := e["time"].(float64); ok {
			ev.Time = &t
		}
		events = append(events, ev)
	}
	return events, nil
}

func deployJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e21 {
			return strconv.FormatFloat(x, 'f', -1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, p := range x {
			if p != nil {
				parts[i] = deployJSString(p)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}
