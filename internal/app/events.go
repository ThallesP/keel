package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

// DockerEvent is what POST /worker/events keeps of a Docker event: enough to name the node it
// concerns (convex/events.ts dockerEvent).
type DockerEvent struct {
	Type        string   // container | service | node | ...
	Action      string   // create | start | die | update | ...
	Name        string   // Actor.Attributes.name ("" when absent)
	ServiceName string   // Actor.Attributes["com.docker.swarm.service.name"] ("" when absent)
	Time        *float64 // time, when a number
}

// IngestWorkerEvents maps agent-forwarded Docker events to scans (events.ingest): resync → one
// full sweep; a `node` event → one server count per batch; a container or service event naming
// svc-<id> → a debounced scan of that node, once per node per batch. Ids that are not ours are
// ignored. Called by the bearer-protected agent route; it does no Docker call itself.
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
		scheduled := a.scheduleObserveFor(ctx, id, observeDebounce, 0)
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
