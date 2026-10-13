# Metrics and the Observability page

> Resource metrics (CPU, memory, network per service; CPU, memory, disk per server) and the rail's Observability page that shows them next to requests and logs (decided 2026-10-13). Read before touching `internal/{domain,app,api}/metrics.go`, `internal/adapters/sqlite/{metrics.go,queries/metrics.sql}`, `internal/transport/http/metrics*.go`, `internal/agent/{metrics,host_*}.go`, `components/canvas/observability` or `components/canvas/charts`.

## The split

- **Observability** (`?view=observability`): a page of panels, one grid per environment. This is where you look first.
- **Logs** (`?view=logs`): requests and log lines in one stream, every row one click from its trace (what the Observability page was until 2026-10-13; [`logs.md`](./logs.md)).
- **Metrics tab** in a service's bottom panel: CPU, memory and network of that service, built from the same chart components.

Metrics need no setup: they work on a fresh install with no Axiom. Requests, latency and log panels read the organization's Axiom sink like the Logs page, and say so when there is none.

## Collection

`keel agent` already runs on every server, so it samples there (`internal/agent/metrics.go`). Every 15 s (`domain.MetricsInterval`), on wall-clock boundaries so every server's samples share a timestamp:

- **Per running `svc-*` container**, from Docker's one-shot stats (`GET /containers/<id>/stats?stream=false&one-shot=true`): CPU as cores used (`cpu_usage.total_usage` delta over the wall-clock delta of `read`), memory in use (`usage` minus `inactive_file`, cgroup v1's `total_inactive_file`, which is what `docker stats` shows), network bytes per second summed over the container's interfaces. The agent keeps the previous reading per container, so a container shows up from its second sample on.
- **The server itself**, from `/proc/stat` (busy over total jiffies × CPUs), `/proc/meminfo` (`MemTotal` − `MemAvailable`) and `statfs` of the agent's state volume, which lives on the disk that holds Docker's data. The agent container sees the host's `/proc/stat` and `/proc/meminfo`: neither is namespaced.

It posts one report per tick to `POST /worker/metrics` (worker bearer, at most 1 MiB):

```json
{"at": 1791460800000, "server": {"id": "<swarm node id>", "name": "<hostname>"},
 "host": {"cpus": 4, "cpuCores": 1.2, "memoryBytes": 0, "memoryTotalBytes": 0, "diskBytes": 0, "diskTotalBytes": 0},
 "tasks": [{"serviceId": "<node id>", "task": "<swarm task id>", "cpuCores": 0.4, "memoryBytes": 0, "rxBytesPerSec": 0, "txBytesPerSec": 0}]}
```

`host` is `null` when the agent could not read it. A failed post drops that sample: metrics are best effort and the next tick is 15 s away. Samples of a node Keel does not know (deleted since) are skipped.

## Storage

SQLite, migration `0002_metrics.sql`: `service_metrics` (one row per task per tick), `server_metrics` (one row per server per tick). Kept 7 days (`domain.MetricsRetention`, the longest range the page offers), pruned hourly by the `metrics.prune` job. No rollups: at 15 s, a service with one replica is ~40k rows a week, and a range query reads one service's index range.

Why not the log sink: metrics have to work before anyone connects Axiom, and the control plane already has a database. Why not Prometheus or OpenTelemetry metrics: one more service to run for four numbers; `OTEL_METRICS_EXPORTER` stays `none`.

## Reading

`GET /api/environments/{id}/metrics?range=15m|1h|24h|7d&nodeId=` → `MetricsOverview`, same buckets as the trace overview (`domain.TimeRange.Spec`), so hovering one chart points at the same bucket on all of them:

- `services[].buckets[]`: replicas summed per tick, then per bucket the **average** CPU and network and the **peak** memory (a spike is what gets a container killed). Only services with samples in range; empty buckets are `null`, drawn as gaps.
- `total[]`: the same over every service in scope (the Network panel draws it as In and Out).
- `servers[]`: servers that ran one of the environment's services in the last minute, with their latest host sample and the services on them. Servers are listed per environment, not per install, so one organization never sees another's machines.
- `deployments[]`: Ships started in range, drawn as dashed marks on every time chart (red when failed).

## Panels

`GET /api/environments/{id}/dashboard` → `{panels}` (the default layout until someone saves one), `PUT` replaces them. A panel is `{id, kind, nodeId, search, wide}`:

| kind | shows | needs |
| --- | --- | --- |
| `cpu`, `memory` | one line per service, or one service | nothing |
| `network` | In and Out | nothing |
| `requests`, `latency` | the trace overview's charts | Axiom with traces |
| `logs` | the latest lines containing `search`, opening in Logs | Axiom |
| `servers` | CPU, memory and disk per server | nothing |

Default: CPU, Memory, Requests, Latency, Network, Servers, then a wide Logs panel for `error`. **Customize** shows a handle (drag to reorder, `@dnd-kit/sortable`), the service picker or search, half/full width and remove on each panel, and an **Add panel** tile; every change saves at once. A panel whose service was deleted reads as every service. At most 24 panels; kinds are validated in `domain.ValidatePanels`.

## Next

- Sparklines in canvas node bodies from the same overview (`docs/agents.md` assumes node bodies carry metrics).
- `keel metrics --json` for agents.
- Alerts: volume or disk at 90 %, OOM kills (Docker's `oom` event already reaches `/worker/events`), server memory exhausted.
- Edge traffic from keel-proxy: request counts and status codes for every exposed service without instrumentation, and the canvas's live traffic edges.
