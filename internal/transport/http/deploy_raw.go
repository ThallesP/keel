package http

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
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
	body, err := io.ReadAll(io.LimitReader(r.Body, workerEventsMaxBody+1))
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > workerEventsMaxBody {
		deployWriteText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	events, err := parseWorkerEvents(body)
	if err != nil {
		deployWriteText(w, http.StatusBadRequest, "bad json")
		return
	}
	s.app.IngestWorkerEvents(r.Context(), events, r.Header.Get("X-Keel-Resync") == "1")
	deployWriteText(w, http.StatusOK, "ok")
}

func deployWorkerAuthorized(r *http.Request, expected string) bool {
	token, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if expected == "" || !isBearer {
		return false
	}
	a, b := sha256.Sum256([]byte(strings.TrimSpace(token))), sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func deployWriteText(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, text)
}

type dockerEvent struct {
	Type   string
	Action string
	Actor  struct {
		Attributes struct {
			Name        string `json:"name"`
			ServiceName string `json:"com.docker.swarm.service.name"`
		}
	}
}

func decodeDockerEvents(body []byte) ([]dockerEvent, error) {
	var events []dockerEvent
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		err := json.Unmarshal(body, &events)
		return events, err
	}
	for dec := json.NewDecoder(bytes.NewReader(body)); dec.More(); {
		var e dockerEvent
		if err := dec.Decode(&e); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func parseWorkerEvents(body []byte) ([]app.DockerEvent, error) {
	docker, err := decodeDockerEvents(body)
	if err != nil {
		return nil, err
	}
	events := make([]app.DockerEvent, 0, len(docker))
	for _, e := range docker {
		if e.Type == "" || e.Action == "" {
			return nil, errors.New("not a Docker event")
		}
		events = append(events, app.DockerEvent{Type: e.Type, Action: e.Action, Name: e.Actor.Attributes.Name, ServiceName: e.Actor.Attributes.ServiceName})
	}
	return events, nil
}
