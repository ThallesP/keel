package app

import (
	"cmp"
	"context"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

type DockerEvent struct {
	Type        string
	Action      string
	Name        string
	ServiceName string
	Time        *float64
}

func (a *App) IngestWorkerEvents(ctx context.Context, events []DockerEvent, resync bool) {
	if resync {
		a.Jobs.After("observe:all", 0, a.observeAll)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type == "node" {
			a.Jobs.After("observe:servers", 0, a.observeServers)
			continue
		}
		if e.Type != "container" && e.Type != "service" {
			continue
		}
		name := e.Name
		if e.Type == "container" {
			name = e.ServiceName
		}
		id, ok := strings.CutPrefix(name, domain.ServicePrefix)
		if !ok || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		scheduled, _ := a.scheduleObserve(ctx, id, observeDebounce, 0)
		a.Log.Info("event", "type", e.Type, "action", e.Action, "name", cmp.Or(e.ServiceName, e.Name), "observeScheduled", scheduled)
	}
}
