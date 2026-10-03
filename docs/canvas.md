# Canvas UI — v1 spec (no agents)

> Reference for whoever (human or agent) implements the deploy canvas. Read this whole file before touching UI code. The agents layer lives in [`agents.md`](./agents.md) and builds on top of this; v1 ships without it.

## What we are building

A self-hosted deploy platform (see `CLAUDE.md`: alternative to Coolify / Dokploy, differentiator is UX and deploy DX). A **project is a canvas**. Services, databases and caches are nodes. Variables wire them together by reference (`${{ postgres.DATABASE_URL }}`). "Ship" deploys the graph.

Railway is the reference for feel. We are not cloning it; we are stealing the good parts (canvas, variable references, deployments list + log) and dropping the clutter.

Product name is **Keel** for now (ship family: keel, hull, slipway, mooring; "OpenShip" was taken). Not final, so treat the wordmark as a placeholder.

## Principles (UX)

1. **Canvas is the hero.** Nothing steals space from it permanently. Detail lives in a collapsible bottom panel, not a right sidebar.
2. **One detail surface.** Selecting a node opens the bottom panel. There is no second inspector. Node-level actions live in a floating toolbar above the selected node.
3. **Minimal chrome.** Topbar = logo / project / environment / Ship / avatar. No status text, no hashes, no timestamps in the topbar.
4. **Wide beats tall.** Logs, deployments, variables render across the full width of the bottom panel.
5. **Runtime on the surface.** Nodes show live state (health, replicas, domain, "deploying · building 41s"), not just config.
6. **No edges.** Wiring is a variable reference, not a line on the canvas. Edges were removed 2026-09-26 (owner decision: the concept did not fit).
7. **One intense colour.** Hyperlink blue is the only accent. Green / amber / red are semantic only.

## Layout

Desktop, 1440 × 900 reference. Everything is `display: flex`.

```
┌──────────────────────────────────────────────────────────────┐
│ Topbar  48px                                                  │
├────┬──────────────────────────────────────────────────────────┤
│Rail│ Canvas (flex: 1)                                         │
│52px│   [Add ⌘N] [Search ⌘K]                    (top-left)     │
│    │                                                          │
│    │   nodes + groups                                         │
│    │                                                          │
│    │   [− 100% +] [fit]                        (bottom-left)  │
│    ├──────────────────────────────────────────────────────────┤
│    │ Bottom panel  320px open / 44px collapsed                │
├────┴──────────────────────────────────────────────────────────┤
│ Status bar  28px                                              │
└──────────────────────────────────────────────────────────────┘
```

### Topbar (48px)

Left: logo glyph + wordmark, `/`, project name, `/`, environment pill (status dot + name + chevron). Right: **Ship** button (primary, `⌘↵` hint), avatar.

Ship button states:

| State | Label | Style |
|---|---|---|
| idle, nothing changed | `Ship` | primary, enabled |
| idle, uncommitted graph changes | `Ship · 3 changes` | primary |
| deploying | `Shipping… 2/3` | primary-strong, spinner, not clickable |
| failed | `Retry` | danger |

### Rail (52px)

Icon-only vertical nav. Top: Canvas (active), Observability (Traces + Logs tabs, see [`logs.md`](./logs.md)), Metrics, Variables. Bottom: Settings. Active item gets `--color-primary-soft` background and primary icon. 32 × 32 hit targets, 6px gap.

### Canvas

- Background `--color-canvas` with 1px dot grid every 20px in `--color-dot`.
- Top-left floating toolbar: `+ Add` button (kbd hint `N`) and a 240px search field (`⌘K`). Both white, 1px `--color-line` border, 6px radius, 1px shadow.
- Bottom-left: zoom group (− / 100% / +), fit-view. Same styling. No minimap: dropped 2026-09-20, not useful at this graph size.
- Pan with space+drag or middle mouse. Scroll = pan, `⌘`+scroll = zoom.

### Groups

A dashed rounded rect (`1px dashed #B9C4E6`, 14px radius, 3% primary tint) with an uppercase 11px label top-left. Used for logical grouping (e.g. "Workers", "Data"). Nodes dragged inside become children and move with the group. In React Flow this is a node with `type: "group"` and children set `parentId` + `extent: "parent"`.

## Nodes

### Shell (shared by every type)

```
┌──────────────────────────────────────┐
│ [icon 26px]  name (13/600)           │   ← header, padding 12 14 0
│              subtitle (11, muted)    │      domain / image / engine, optional
│                                      │
│ ●●● Online                           │   ← status line, sans 12px, status color
├──────────────────────────────────────┤
│ ▭ footer strip (mono 11, optional)   │   ← e.g. attached volume, later
└──────────────────────────────────────┘
 handles: 9px circles, 1.5px border, centered at y=34 (name line)
```

Railway-style: the icon says what the node is, so there is no `Service · Docker` kind label and no corner status dot. Status is one line in the body whose leading glyph doubles as the replica count.

- Width: 220px (infra) / 242px (things with chips, see agents.md).
- Background `--color-bg`, border `1px solid --color-line`, radius 10px.
- Shadow: `0 1px 2px rgba(11,18,32,.05), 0 4px 12px rgba(11,18,32,.04)`.
- **Selected**: border `1.5px --color-primary`, ring `0 0 0 3px rgba(31,75,255,.14)`, handles switch to primary border.
- Icon tile: 26px, 7px radius, `#F0F2F5` background, 14px stroke icon in `--color-ink`.
- Status line glyph: 6px dot, green healthy, blue deploying (pulsing), red error, grey stopped, grey outline pending. Runtime nodes with `replicas > 1` show one dot per replica (`●●○` = 2 of 3 running, capped at 6); never a `2/3 replicas` text line on the card.

### Types (v1)

| type | icon | subtitle | body lines | handles |
|---|---|---|---|---|
| `service` | `< >` | domain if public, else the image in mono (`ghcr.io/acme/api:1.2`) | status line. Port and replica counts live in the panel, not on the card. | target left, source right |
| `database` | cylinder | `Postgres 16` | status line | target left |
| `cache` | stacked rects | `Redis 7` | status line | target left |
| `volume` | disk | `10 GB` | `○ Not mounted` | target left |

Add more later (queue, cron, static site). Every type is the same shell with a different icon and body. Do not invent a new shell per type.

### Node states

| state | status line | border |
|---|---|---|
| healthy | `●●● Online` (success) | default |
| done | `✓ Completed 4s ago` (success; one-shot image, nothing running) | default |
| deploying | `● Deploying · <step> <elapsed>` (primary, dot pulses) | default |
| error | `●○○ Crashed` (danger) + `crash loop · exit 137 (OOM)` on a `--color-danger-soft` pill with a "Logs" link | `--color-danger` |
| stopping | `● Stopping…` (faint, dot pulses) | default |
| stopped | `● Stopped 2h ago` (faint) | default |
| pending | `○ Not deployed` (faint) | dashed |

## Variable references (replaces edges)

A variable value may contain `${{ node.KEY }}` (another node in the environment, by name) or `${{ KEY }}` (same node). References resolve at apply time (`variables.computeEnv`), so a ship always sees current values; chains resolve up to 5 deep, a missing target resolves to `""`.

Keys a node answers to: its own variables, plus generated ones that are never stored — `DATABASE_URL` (database, built from its creds), `REDIS_URL` (cache), `URL` (service with a port), and `HOST` / `PORT` (every runtime node; `HOST` is `svc-<id>` on the overlay).

Keeping references valid: node names are unique per environment; renaming a node rewrites `${{ old.KEY }}` everywhere; renaming a variable key rewrites references to it; changing a node's variables or port marks every node that references it dirty (so Ship picks them up); deleting a node leaves its references red.

## Selection → floating node toolbar

Appears 10px above the selected node, centered. 30px tall, white, 1px border, 7px radius, `0 2px 8px rgba(11,18,32,.08)` shadow.

Items: `Redeploy` · `Logs` · `Restart` · divider · `⋯` (rename, duplicate, delete). Use React Flow's `<NodeToolbar>` component; it handles positioning and zoom.

## Bottom panel

Opens when a node is selected. `Esc` or clicking the canvas background collapses it to a 44px strip that still shows the node name and tabs.

```
┌──────────────────────────────────────────────────────────────┐
│ [icon] service-name ●   Deployments  Variables  Logs      … meta  ⌄ │ 44px
├──────────────┬───────────────────────────────────────────────┤
│ left column  │ main (flex: 1)                                 │
│ 300px        │                                                │
└──────────────┴───────────────────────────────────────────────┘
```

Tabs are 13px, active = ink + 2px underline. Right side of the header shows small mono meta (`2 replicas · us-east-1`) only while collapsed, plus a collapse chevron. Deployments is the default tab: it answers "what is running, is it healthy, what happened last". There is no Overview tab; the facts it would hold live in a one-line meta strip at the top of Deployments, and editable config belongs to a future Settings tab.

| tab | left column | main |
|---|---|---|
| Deployments | 380px. Meta strip above both columns: mono `image · port n · 1/1 replica … status`. Then the current deployment as a card (status pill `ACTIVE / DEPLOYING / FAILED / STOPPED`, message, `21h ago · 9s`, `View logs` → Logs tab), then `HISTORY` rows (`REMOVED` pill, message, age). Railway-style. | selected deployment: step list (see Ship flow) + build log |
| Variables | — | Composer row pinned on top (`KEY` · value with inline **Reference** button · secret toggle · `Add ↵`; paste `KEY=value` splits). Reference opens a `Palette`: node → key, inserts `${{ node.KEY }}` at the caret and prefills the key. Services get a `Connect` strip of one-click chips (`+ postgres.DATABASE_URL`) for nodes they do not reference yet. Rows: key, value with reference chips (click → jump to that node) and `→ resolved` (masked when anything secret is involved), hover reveals edit / delete; double-click edits in place. |
| Logs | — | one full-width stream, every replica merged and sorted by time, each line tagged `r<slot>` in a per-replica muted hue (from Docker `details=true` task ids). Mono 11px / 19px line-height, timestamps left, "following" indicator top-right, Raw toggle (ISO stamp + task id + stream). No replica selector: dropped 2026-09-20. |

Logs must use `white-space: pre` and a monospace font. Never wrap log lines; scroll horizontally.

## Ship flow

Clicking **Ship** diffs the graph against the last deployed revision and starts a deployment. There is no separate drawer: a deployment is a row on the **Deployments tab**, and the tab is the only place it renders.

- The selected deployment is the URL: `/p/<slug>?deployment=<id>`. Ship, every node action (Deploy / Start / Stop / Redeploy / Add), the status bar's `● shipping 12s` / `● last ship 4d` link and the Deployments rows all set it. Reload lands on the same view.
- Opening a deployment opens the Deployments tab of an affected node (the node already in the panel if it took part, else the first step) with that row highlighted. Detail column = step list (16px status circle: green check / blue spinner / grey outline / red ×, name, elapsed) + build log, following with a blinking primary cursor while running.
- Progress while running lives in the topbar Ship button (`Shipping… 2/3 · 41s`) and the status bar link; the tab's log follows.
- Selecting a node that did not take part drops the id from the URL; a stale or foreign id is dropped too.

## Status bar (28px)

Left: health counts as dot + label pairs (`● 5 healthy  ● 1 deploying  ● 1 error`). Right: mono `● last ship 4d · 2 servers · 100%` (latest deployment link, servers, zoom). All 11px. Clicking a health count selects those nodes; clicking the ship link selects it on the Deployments tab.

## Design tokens

Copy these into `packages/ui/src/styles/globals.css` (Tailwind v4 `@theme`). They coexist with the shadcn oklch tokens already there; use these for canvas surfaces and shadcn tokens for generic form controls.

```css
:root {
  --color-bg: #FFFFFF;
  --color-canvas: #F5F6F8;
  --color-surface-2: #F0F2F5;
  --color-ink: #0B1220;
  --color-muted: #5B6472;
  --color-faint: #8A93A1;
  --color-line: #E3E6EB;
  --color-dot: #D5D9E0;
  --color-primary: #1F4BFF;
  --color-primary-strong: #1539CC;
  --color-primary-soft: #E9EEFF;
  --color-on-primary: #FFFFFF;
  --color-success: #12A150;  --color-success-soft: #E4F6EA;
  --color-warning: #D99A00;  --color-warning-soft: #FFF4D6;
  --color-danger: #E23D3D;   --color-danger-soft: #FDE8E8;
  --color-ink-dark: #0A1A4A;  /* blueprint theme ground */
  --color-blueprint: #0F2A7A; /* blueprint theme canvas */

  --font-sans: "Inter Variable", Inter, sans-serif;
  --font-mono: "JetBrains Mono", ui-monospace, monospace;
  --text-2xs: 11px; --text-xs: 12px; --text-sm: 13px; --text-base: 14px;
  --text-md: 16px; --text-lg: 20px; --text-xl: 28px;
  --radius-sm: 4px; --radius-md: 6px; --radius-lg: 10px; --radius-full: 999px;
}
```

Type rules: Inter for UI, JetBrains Mono for anything machine-ish (ids, domains, metrics, logs, kbd hints). Headings 600 weight with `-0.02em` tracking. Uppercase section labels are 11px / 600 / `0.06em`. Nothing below 10px, and 10px only inside chips.

Mood word: **hypertext**. Pure white ground, hyperlink blue accent. Not navy, not gradients.

### Blueprint dark (optional theme)

Same layout, ground `--color-ink-dark`, canvas `--color-blueprint` with a 24px line grid at 7% white. Nodes are ground-coloured with 28% white borders; the selected node inverts to a white card. Use as a theme toggle or marketing only. Do not build it before light mode is done.

## React Flow mapping

Package: `@xyflow/react` (v12). Not installed yet.

| Concept | React Flow |
|---|---|
| node shell + types | `nodeTypes = { service, database, cache, volume, group }`, each a component receiving `NodeProps<Node<Data>>` |
| groups | node `type: "group"`; children have `parentId` and `extent: "parent"` |
| dot grid | `<Background variant="dots" gap={20} size={1} color="var(--color-dot)" />` |
| controls | custom component using `useReactFlow()` (`zoomIn`, `zoomOut`, `fitView`); do not use the default `<Controls />` styling |
| floating toolbar | `<NodeToolbar isVisible={selected} position={Position.Top} offset={10}>` inside the node component |
| selection → panel | `onSelectionChange` → store selected node id → bottom panel reads it |

No edges, no handles: `nodesConnectable={false}`. Set `fitView` on first load with `padding: 0.2`.

## Data model (sketch)

Convex tables. Keep it small; the schema in `packages/backend/convex/schema.ts` is currently empty.

```ts
projects:     { name, slug, ownerId }
environments: { projectId, name, isProduction }
nodes:        { environmentId, type: "service"|"database"|"cache"|"volume"|"group",
                name, parentId?, position: {x,y}, config: any,
                status: "healthy"|"done"|"deploying"|"error"|"stopped"|"pending" }
deployments:  { environmentId, sha, message, status, startedAt, finishedAt,
                steps: { nodeId, status, startedAt, finishedAt }[] }
variables:    { nodeId, key, value, secret: boolean }   // value may hold ${{ node.KEY }}
```

Node `position` is written on drag end (debounced), never on every move.

## Where to start (in this repo)

Stack: Vite + React 19 + TanStack Router (`apps/web`), Convex (`packages/backend`), shadcn in `packages/ui`, Tailwind v4, Bun + Turborepo.

1. `bun add @xyflow/react -F web`. Import `@xyflow/react/dist/style.css` once in `apps/web/src/index.css`.
2. Add the tokens above to `packages/ui/src/styles/globals.css`. Load Inter Variable and JetBrains Mono (Fontsource or Google Fonts).
3. Route: `apps/web/src/routes/_auth/p/$projectId.tsx`. Full-bleed, no `container`. The existing `Header` component is a placeholder; replace it with the topbar spec for this route (or render the canvas route outside the current root grid).
4. Components, all under `apps/web/src/components/canvas/`:
   - `canvas.tsx` — `<ReactFlow>` wrapper, providers, background, controls
   - `nodes/node-shell.tsx` — the shared shell (header, body slot, handles, selected ring, `NodeToolbar`)
   - `nodes/service-node.tsx`, `database-node.tsx`, `cache-node.tsx`, `volume-node.tsx`, `group-node.tsx`
   - `toolbar.tsx` (Add + Search), `controls.tsx` (zoom / fit)
   - `bottom-panel/panel.tsx`, `tabs/deployments.tsx`, `variables.tsx`, `logs.tsx`
   - `topbar.tsx`, `rail.tsx`, `status-bar.tsx`
5. Start with **static fixture data** (the six-node graph from the mockup: trigger-ish webhook → api → postgres / redis). Get the canvas, shell, edges, selection, panel looking right before wiring Convex.
6. ~~Then Convex: schema above, `nodes.list`, `nodes.move`, `edges.connect`, `deployments.start`. Subscribe with `useQuery`; the canvas re-renders from the DB.~~ Done 2026-09-14 (`packages/backend/convex/{projects,environments,nodes,edges,variables,deployments}.ts`, `apps/web/src/components/canvas/{mapping,actions,use-data}.ts*`). Node `status` is derived server-side from `desired`/`observed`, not stored.
7. ~~Ship flow last. Fake the build log with a timer before there is a real builder.~~ Done 2026-09-14. No fake log: the Deployments tab streams `swarm.apply` (pull, create/update) and `swarm.observe` (replicas running) lines from the `deployments` row. There is still no builder; everything is image-based.

Order matters: shell → edges → panel → data → deploy.

## Out of scope for v1

- Agents, tools, runs, traces, approvals → `agents.md`
- Metrics tab and live traffic edges
- Templates / marketplace
- Multi-environment diff view
- Blueprint dark theme
- Mobile / tablet layouts

## Reference

Paper file **"*Ship*"**, artboards:
- `Keel — Canvas (light)` — original with right sidebar (superseded)
- `Keel — Canvas, no sidebar (bottom panel = inspector + logs)` — **the target layout**
- `Keel — Ship in progress (deploy drawer)` — deploy flow
- `Keel — Node kit` — node shell, types, states, edges
- `Keel — Canvas (blueprint dark)` — dark variant

When pulling values, use the Paper MCP `get_jsx` / `get_computed_styles` on the node, not screenshots.
