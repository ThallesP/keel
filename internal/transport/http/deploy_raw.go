package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/ThallesP/keel/internal/app"
)

func (s *Server) registerDeployRaw(mux *http.ServeMux) {
	mux.HandleFunc("POST /worker/events", s.workerEvents)
}

const workerEventsMaxBody = 256 << 10

func (s *Server) workerEvents(w http.ResponseWriter, r *http.Request) {
	if !s.workerAuthorized(r) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workerEventsMaxBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeText(w, http.StatusRequestEntityTooLarge, "too large")
		return
	}
	if err != nil {
		writeText(w, http.StatusBadRequest, "bad json")
		return
	}
	events, err := parseWorkerEvents(body)
	if err != nil {
		writeText(w, http.StatusBadRequest, "bad json")
		return
	}
	s.app.IngestWorkerEvents(r.Context(), events, r.Header.Get("X-Keel-Resync") == "1")
	writeText(w, http.StatusOK, "ok")
}

type dockerEvent struct {
	Type  string
	Actor struct {
		Attributes struct {
			Name        string `json:"name"`
			ServiceName string `json:"com.docker.swarm.service.name"`
		}
	}
}

func parseWorkerEvents(body []byte) ([]app.DockerEvent, error) {
	var docker []dockerEvent
	if err := json.Unmarshal(body, &docker); err != nil {
		return nil, err
	}
	events := make([]app.DockerEvent, len(docker))
	for i, e := range docker {
		if e.Type == "" {
			return nil, errors.New("not a Docker event")
		}
		events[i] = app.DockerEvent{Type: e.Type, Name: e.Actor.Attributes.Name, ServiceName: e.Actor.Attributes.ServiceName}
	}
	return events, nil
}
