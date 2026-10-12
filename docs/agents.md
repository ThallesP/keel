# Agents layer — spec (builds on canvas.md)

> Read [`canvas.md`](./canvas.md) first. This document only describes what **changes or gets added** when agents become first-class. Everything not mentioned here stays as in v1. Do not start this before canvas v1 (shell, edges, panel, drawer, deploy) works end to end.

## Thesis

**Agents are deploy units, same as services.** An agent is a node on the same canvas, wired with the same edges, shipped with the same button, inspected in the same bottom panel. It is not a workflow step, not a separate product, not a chat window.

Two things make an agent node different from a service node:

1. It can be defined **without code** (prompt + model + tools), and we host the runtime.
2. Its observability is **traces**, not just logs: turns, tool calls, tokens, cost, handoffs.

And one thing makes the platform "agents-first" rather than "agents-supported": **agents are users too.** The platform exposes itself as a CLI and an MCP server, so an agent can deploy, restart, read logs, or spin up another agent.

## What changes vs canvas.md

| Area | v1 | with agents |
|---|---|---|
| node types | service, database, cache, volume, group | + `agent`, `tool`, `approval`, `memory`, `trigger` |
| edge kinds | connection, pending, traffic | + `handoff` (agent → agent), `tool` (agent → tool/service/db), `gate` (agent → approval) |
| node body | metrics | agents show tool chips + `runs/m · p50 · $/h` |
| node states | healthy / deploying / error / stopped / pending | agents add `running`, `waiting on human` |
| bottom panel tabs | Overview, Variables, Deployments, Logs | agents: Overview, **Tools**, Variables, **Runs**, Logs (Logs = trace view) |
| Add menu | service / database / cache / volume | + **Add agent** modal with three paths |
| status bar | health counts, region, cost | + `N approvals pending` (amber) |
| Ship | deploys containers | also deploys hosted-runtime agents (prompt + config, no build step) |

## New node types

All use the v1 shell. Width 242px for `agent` (chips need room), 220px for the rest.

| type | icon tile | subtitle | body | handles |
|---|---|---|---|---|
| `agent` | robot glyph on **primary** tile (only node with a coloured tile) | `Agent · claude-sonnet-5` | row of tool chips (mono 10px, `#F0F2F5` pills), then `38 runs/m  p50 1.8s  $0.42/h` | target left, source right, source bottom (handoff) |
| `trigger` | bolt | `Trigger · Webhook` / `Trigger · Schedule` / `Trigger · Queue` | `POST /hooks/slack/events` + `1.2k events · 24h`, or cron + `next in 6h` | source right only |
| `tool` | wrench | `Tool · MCP server` | `3 tools exposed` + `used by 2 agents` | target left |
| `approval` | warning triangle on **warning-soft** tile | `Human approval` | condition (`amount > $200`) + `2 waiting · Slack #ops` in amber | target left, source right |
| `memory` | three linked circles | `Memory · pgvector` | `412k chunks · 1536d` + `synced 12m ago` | target left |

Databases and caches from v1 double as tools when an agent connects to them (see edges).

## Agent node states

| state | dot | body extra | border |
|---|---|---|---|
| idle | green | — | default |
| running | blue with 3px soft ring | primary-soft pill: spinner + `calling web-search` + elapsed; below: `3 active · turn 4/12` | `--color-primary` |
| waiting on human | amber | warning-soft card: question (`Refund $340 to cust_91ab?`) + inline **Approve** (ink) / **Deny** (outline) buttons | default |
| error | red | danger-soft pill: `crash loop · exit 137 (OOM)` + "Logs" link; below `restarted 4× · 512 MB limit` | `--color-danger` |

Approve/Deny on the node must work without opening the panel. This is the main reason approvals are nodes, not settings.

## Edge semantics

Edges do the wiring. Drawing one registers a tool or injects config; the user never writes a tool schema for things that already exist on the canvas.

| from → to | kind | stroke | what happens on connect |
|---|---|---|---|
| trigger → agent | `connection` | primary, solid | trigger payload becomes the agent's input |
| agent → database / cache | `tool` | grey, solid | a read tool is registered on the agent (`search_tickets` over postgres); env var injected too |
| agent → service | `tool` | grey, solid | service's declared endpoints become tools (`get_customer` from `support-api`) |
| agent → tool node | `tool` | grey, solid | MCP server's tools appear as chips |
| agent → agent | `handoff` | primary, `1 5` dotted, round caps, label pill `handoff` in primary-soft | source agent gets a `handoff` tool targeting the destination |
| agent → approval | `gate` | grey, solid | runs pause at the gate when the condition matches |
| any → not-yet-shipped node | `pending` | grey, dashed `4 4` | as v1 |

Chips on an agent node map 1:1 to its outgoing `tool` / `handoff` edges. Hovering a chip highlights the edge and target node.

## Add agent (three doors, one node)

`+ Add → Agent` opens a 760px modal titled **Add agent**, subtitle "Three ways in. All end up as the same node on the canvas."

| path | who | what the user supplies | what we do |
|---|---|---|---|
| **Describe it** (highlighted, "fastest") | no-code, ops, PMs, sub-agents | one-paragraph description, model picker, tools picked by clicking existing canvas nodes | generate system prompt draft, create node with `runtime: "hosted"`, run it on our agent runtime (Claude Agent SDK underneath) |
| **From a repo** ("bring your code") | engineers | repo URL / branch | detect agent SDK, read declared tools, wire secrets, deploy on push; node has `runtime: "container"` |
| **From a template** ("community") | anyone | pick from list (`support-triage`, `research-swarm`, `code-reviewer`) | drop a whole pre-wired subgraph (agent + memory + gate + tools) onto the canvas |

Footer: `Or from your terminal` + `keel ship ./triage-agent` + link "Agents can call this too →".

**Same node either way.** A hosted agent and a repo agent look identical on the canvas and in the panel. The only visible difference is the Overview tab: hosted shows an editable prompt, repo shows the source. A hosted node has an **Eject to repo** action that scaffolds an SDK project from its config. Nobody gets trapped.

### When "Describe it" is the right door

- **Glue between things already on the canvas.** "When a Slack ticket arrives, look up the customer in postgres, summarize, post to #support." Prompt is the entire logic; a repo would be pure boilerplate.
- **Ops / on-call agents.** "Every 5 min check `support-api` logs for 5xx; restart and page #ops with the last 20 lines." Tools are the platform's own API. Nobody writes a repo for that.
- **Cheap sub-agents / handoff targets.** Main agent lives in a repo. It needs `billing-agent`: "use `stripe.refund`, refunds over $200 need approval, confirm order id first." Four lines + a gate node. Cheap sub-agents → people actually split agents instead of growing one bloated one.
- **Try before build.** Describe it, wire to staging, run 50 tickets, read traces. Works → eject to repo. Doesn't → delete node. Ten minutes, not a sprint.
- **Non-engineer prompt owners.** Support lead edits the prompt in Overview; devs own the tools via edges. Same split as a PM editing an env var without a PR.

### When it is the wrong door

- Custom tool logic (a prompt cannot call what does not exist) → repo.
- Complex control flow, retries, state machines → repo.
- Anything that needs tests → repo.

## Bottom panel additions

Agent nodes get these tabs: **Overview · Tools · Variables · Runs · Logs**.

| tab | left column (300px) | main |
|---|---|---|
| Overview | — | model select (mono), Temperature / Max tokens / Replicas in a 3-column row, system prompt (mono 11px on `--color-surface-2`, header shows `prompts/triage.md` for repo agents), runtime badge (hosted / container) |
| Tools | list of tools: fixed 18px icon slot, mono name, right-aligned source node name in faint | selected tool: schema, source edge, last 5 calls |
| Variables | as v1 | as v1 |
| Runs | run rows: 8px status dot, mono id (64px), title, duration, age. Selected row gets `--color-primary-soft` | the selected run's trace (same view as Logs) |
| Logs | replica / run selector | trace stream |

### Trace line format

Traces are the agent equivalent of logs. Full width, mono 11px, 19px line-height, `white-space: pre`, fixed columns:

```
HH:MM:SS.mmm  kind     detail    payload                                  timing
12:04:31.020  in       slack     "I was charged twice this month…"
12:04:31.118  turn 1   model     claude-sonnet-5 · 1.2k in · 84 out       0.6s
12:04:31.740  tool     call      search_tickets({ customer: "cust_91ab" })
12:04:31.902  tool     result    2 tickets · postgres                     162ms
12:04:32.480  handoff  to        billing-agent · "duplicate charge"
12:04:32.481  done               1.4s · 3.1k tokens · $0.004
```

Colour: default `--color-muted`; `handoff` lines in `--color-primary`; `done` in `--color-ink`; errors in `--color-danger`. Header row above the stream: run id, `trace · 7 events · $0.004`, and `Replay` / `Raw` / `following` on the right.

## Approvals

- Node-inline Approve / Deny (see states).
- Status bar shows `● 2 approvals pending` in amber; clicking selects the waiting nodes.
- Each pending approval also posts to the configured Slack channel with the same two buttons. Both surfaces resolve the same record.

## Cost

Every agent node shows `$/h` in its body. Status bar shows `$ today` for the environment. Runs tab shows `$` per run. Cost = model tokens + hosted runtime time; container agents only count runtime.

## The platform as a tool

Ship a CLI and an MCP server exposing the same API:

```
keel ship [path]           deploy current dir (or a hosted agent from a .keel/agent.yaml)
keel logs <node> [--follow]
keel restart <node>
keel runs <agent> [--last 10]
keel agent create --describe "..." --model ... --tools node1,node2
```

MCP tools mirror these: `deploy`, `logs`, `restart`, `list_nodes`, `create_agent`, `approve`. An agent running on the platform gets the MCP server as a tool by default when the user opts in (a `tool` edge from the agent to a `keel` platform node makes it explicit on the canvas).

## Data model additions

```ts
nodes.type       += "agent" | "tool" | "approval" | "memory" | "trigger"
nodes.config     (agent) { runtime: "hosted" | "container", model, temperature,
                           maxTokens, replicas, systemPrompt?, repo?, tools: ToolRef[] }
edges.kind       += "handoff" | "tool" | "gate"

runs:         { nodeId, status: "running"|"done"|"error"|"waiting", startedAt,
                finishedAt, tokensIn, tokensOut, costUsd, title, triggerId? }
traceEvents:  { runId, ts, kind: "in"|"turn"|"tool_call"|"tool_result"|"handoff"|"done"|"error",
                detail, payload: any, durationMs? }
approvals:    { runId, nodeId, question, status: "pending"|"approved"|"denied",
                decidedBy?, decidedAt?, channelMessageId? }
```

`traceEvents` will be large. Index on `(runId, ts)` and paginate the Logs tab.

## Open questions (decide before building)

1. **Hosted runtime.** Are we running the "Describe it" agents ourselves in v2, or is v2 repo-only with the modal showing Describe-it as "coming soon"? Repo-only is a valid first cut; the node shape does not change.
2. **Trace format.** OpenTelemetry GenAI semantic conventions (interop, existing exporters) vs our own schema (simpler, matches the table above). Leaning OTel-compatible storage with our own render.
3. **Model providers.** Anthropic first. Keep `model` a string, not an enum.
4. **Where does the Claude Agent SDK run** for hosted agents — same worker fleet as containers (Tailscale mesh, per `CLAUDE.md`) or a dedicated pool?

## Where to start (after canvas v1)

1. Add the five node types to `nodeTypes`, reusing `node-shell.tsx`. Agent node first; it is the only one with new anatomy (chips, state pills, bottom handle).
2. Add `handoff`, `tool`, `gate` to `edgeTypes`. Handoff edge = dotted primary bezier + `EdgeLabelRenderer` pill.
3. `onConnect` rules: derive `kind` from source/target types, register tool chips on the agent, open the Tools tab.
4. Agent tabs in the bottom panel: Overview form, Tools list, Runs list + trace view. Build the trace view with fixture data from the format above.
5. Add agent modal (three cards + CLI footer). Wire "From a repo" first if hosted runtime is undecided.
6. Approvals: node-inline buttons + status bar count. Slack later.
7. Backend (`keel serve`): `runs`, `trace_events`, `approvals` tables (a new migration in `internal/adapters/sqlite/migrations/`); use cases in `internal/app` and `/api` routes to list runs, read a run's trace (refetched on its realtime topic while it runs) and decide an approval. Was planned as Convex `runs.list`, `traces.stream`, `approvals.decide`.
8. CLI / MCP last; it is a thin client over the same HTTP API.

## Reference

Paper file **"*Ship*"**, artboards:
- `Keel — Node kit` — agent states (idle / running / waiting / error), trigger / tool / approval / memory nodes, four edge kinds
- `Keel — Canvas, no sidebar` — agent group, handoff edge, Runs + trace in the bottom panel
- `Keel — Add agent (three paths)` — the modal
- `Keel — Canvas (light)` — Tools tab and agent Overview form (in the superseded right sidebar; port the content, not the placement)
