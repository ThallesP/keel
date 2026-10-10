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

const workerEventsMaxBody = 256 * 1024

func (s *Server) workerEvents(w http.ResponseWriter, r *http.Request) {
	if !s.workerAuthorized(r) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, workerEventsMaxBody+1))
	if err != nil {
		writeText(w, http.StatusBadRequest, "bad json")
		return
	}
	if len(body) > workerEventsMaxBody {
		writeText(w, http.StatusRequestEntityTooLarge, "too large")
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
	Type   string
	Action string
	Actor  struct {
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
		if e.Type == "" || e.Action == "" {
			return nil, errors.New("not a Docker event")
		}
		events[i] = app.DockerEvent{Type: e.Type, Action: e.Action, Name: e.Actor.Attributes.Name, ServiceName: e.Actor.Attributes.ServiceName}
	}
	return events, nil
}
