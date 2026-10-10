package app

import (
	"context"
	"fmt"
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
			if !seen["node"] {
				seen["node"] = true
				a.Jobs.After("observe:servers", 0, a.observeServers)
			}
			continue
		}
		if e.Type != "container" && e.Type != "service" {
			continue
		}
		name := e.Name
		if e.Type == "container" {
			name = e.ServiceName
		}
		if !strings.HasPrefix(name, domain.ServicePrefix) {
			continue
		}
		id := strings.TrimPrefix(name, domain.ServicePrefix)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		scheduled, _ := a.scheduleObserve(ctx, id, observeDebounce, 0)
		label := e.ServiceName
		if label == "" {
			label = e.Name
		}
		verdict := "skipped"
		if scheduled {
			verdict = "scheduled"
		}
		a.Log.Info(fmt.Sprintf("event %s %s %s → observeNode %s", e.Type, e.Action, label, verdict))
	}
}
