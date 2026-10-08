# Porting spec index

Where each piece of today's backend (`packages/backend/convex`, Better Auth, `apps/worker`,
`apps/proxy`) is specified for the Go rewrite. "Primary" is the spec that owns the full contract
(args, return shape, access rule, validation order, exact errors, side effects); "Also" lists
specs that describe it from their angle (callers, realtime keys, Go route, CLI mapping).

| Spec | Scope |
| --- | --- |
| [auth-orgs.md](auth-orgs.md) | Better Auth tables and endpoints, sessions, sign-up/founding/joining, invitations, device login, `access.ts`, HTTP router |
| [projects.md](projects.md) | projects, environments, canvas nodes, variables and references, dirty/Ship, deployments, reconcile, Swarm spec |
| [swarm-worker.md](swarm-worker.md) | Swarm reconciler (apply/observe), Docker events, deployments settle/timeout, the per-node agent, crons |
| [observability.md](observability.md) | log sinks, Sign in with Axiom, `logs.*`, `traces.*`, `tracing.*`, OTLP relay, agent log shipping |
| [proxy-ingress.md](proxy-ingress.md) | endpoints (expose/unexpose), keel-proxy sync, Caddy config, cert reports, `apps/proxy` |
| [web-data.md](web-data.md) | every web call site, JSON shapes the web reads, optimistic updates, realtime topics (canonical, §10) |
| [cli-install.md](cli-install.md) | `keel` CLI contract and the Convex calls it makes, `install.sh`, compose, CI, Go-binary layout |

Cross-spec conflicts found by the completeness pass are written up in the addenda: routes,
response envelopes and realtime key names in **web-data.md "Addendum (critic)" W1–W2**; server
error codes and env var names in **cli-install.md "Addendum (critic)" C1–C2**; operational log
lines in **swarm-worker.md "Addendum (critic)" A1**. Resolve W1/C1/C2 before writing handlers.

Legend: P = public (callable by clients), I = internal. Kinds: Q query (reactive in the web
unless noted), M mutation, A action (never reactive), IQ/IM/IA internal query/mutation/action,
H HTTP action, C cron, S helper scheduled by `ctx.scheduler`.

## 1. Convex functions → spec

### 1.1 Auth, organizations, leftovers

| Function | Kind | Primary | Also |
| --- | --- | --- | --- |
| `auth.getCurrentUser` | P Q | auth-orgs §7.7 | web-data §4.1 Q2/Q22/Q24, §5.6; cli-install A11 |
| `auth.signUpOpen` | P Q (public) | auth-orgs §6.2 | web-data §4.1 Q1 |
| `organizations.current` | P Q | auth-orgs §7.5 | web-data §4.1 Q3/Q5; cli-install A11; observability §4.5 (membership gate) |
| `organizations.invitation` | P Q (public) | auth-orgs §7.6 | web-data §4.1 Q25 |
| `privateData.get` | P Q (template leftover, do not port) | auth-orgs §7.8 | — |
| `healthCheck.get` | P Q (template leftover) | auth-orgs §7.8 | — |
| Better Auth endpoints `/api/auth/*` (sign-up, sign-in, sign-out, get-session, convex/token, convex/jwks, organization/invite-member, organization/accept-invitation, device/code, device/token, device, device/approve, device/deny) and database hooks | H (component routes) | auth-orgs §5, §6.1, §7.2–7.4 | web-data §4.4; cli-install A8 |

### 1.2 Projects, environments, nodes, variables, deployments

| Function | Kind | Primary | Also |
| --- | --- | --- | --- |
| `projects.ensureDefault` | P M | projects §9.1 | auth-orgs §6.3 (founding, legacy adoption); web-data §4.2 M1 |
| `projects.create` | P M | projects §9.1 | auth-orgs §6.3; web-data M2; cli-install A11 |
| `projects.getBySlug` | P Q | projects §9.1 | web-data Q23 |
| `projects.list` | P Q | projects §9.1 | auth-orgs §6.3 (no-membership rule); web-data Q21; cli-install A11 |
| `environments.summary` | P Q | projects §9.2 | swarm-worker §17; web-data Q12; cli-install A11 |
| `environments.setServers` | IM | projects §9.2 | swarm-worker §8.6 |
| `nodes.list` | P Q | projects §9.3, §4.3 (`NodeView`) | web-data Q9–Q11, §5.1; proxy-ingress §3.7 (`endpointView`); cli-install A11 |
| `nodes.create` | P M | projects §9.3 | web-data M3; cli-install A11 |
| `nodes.move` | P M | projects §9.3 | web-data M10, §7 (optimistic) |
| `nodes.rename` | P M | projects §9.3, §5.5 | web-data M4 |
| `nodes.setDesired` | P M (no caller today) | projects §9.3 | — |
| `nodes.stop` | P M | projects §9.3 | web-data M9 |
| `nodes.start` | P M | projects §9.3 | web-data M8 |
| `nodes.expose` | P M | proxy-ingress §4.1 | projects §9.3; web-data M12 |
| `nodes.unexpose` | P M | proxy-ingress §4.2 | projects §9.3; web-data M13 |
| `nodes.publicAddress` | P Q (static) | proxy-ingress §4.3 | projects §9.3; web-data Q16 |
| `nodes.duplicate` | P M | projects §9.3 | proxy-ingress §4.4; web-data M5 |
| `nodes.remove` | P M | projects §10 | proxy-ingress §4.4; swarm-worker §7; web-data M11, §7; cli-install A11 |
| `variables.list` | P Q | projects §9.4, §5 | web-data Q18, §5.4; cli-install A11 |
| `variables.referenceable` | P Q | projects §9.4 | web-data Q19 |
| `variables.set` | P M | projects §9.4 | web-data M14; cli-install A11 |
| `variables.remove` | P M | projects §9.4 | web-data M15; cli-install A11 |
| `deployments.start` | P M (`beginDeployment`) | projects §9.5, §7.1 | swarm-worker §10.1; web-data M6/M7; cli-install A11 |
| `deployments.latest` | P Q | projects §9.5 | swarm-worker §17; web-data Q13, §5.3; cli-install A11 |
| `deployments.get` | P Q | projects §9.5 | web-data Q14; cli-install A10.2, A11 |
| `deployments.listForNode` | P Q | projects §9.5 | web-data Q15; cli-install A11 |
| `deployments.stepRunning` | IM | swarm-worker §6.3 | projects §7.2 |
| `deployments.stepLog` | IM | swarm-worker §6.3 | projects §7.2 |
| `deployments.stepApplied` | IM | swarm-worker §6.3 | projects §7.2 |
| `deployments.stepFailed` | IM | swarm-worker §6.3 | projects §7.2 |
| `reconcile.run` | IM | swarm-worker §10.2 | projects §7.4 |
| `reconcile.timeoutDeployment` | IM (scheduled 5 min after each deployment) | swarm-worker §10.3 | projects §7.5 |
| `migrations.run` | IM (every install/upgrade) | projects §9.9 | proxy-ingress §5.7; swarm-worker §16.1 |

### 1.3 Swarm reconciler, Docker events, worker

| Function | Kind | Primary | Also |
| --- | --- | --- | --- |
| `swarm.apply` | IA | swarm-worker §6.1 | projects §7.2, §8 |
| `swarm.remove` | IA | swarm-worker §6.6 | projects §9.8, §10 |
| `swarm.removeLegacyTunnels` | IA | proxy-ingress §5.8 | swarm-worker §6.7 |
| `swarm.observeNode` | IA | swarm-worker §8.3 | projects §8.5 |
| `swarm.observeSwarmNodes` | IA | swarm-worker §8.6 | projects §8.5 |
| `swarm.observe` | IA (full sweep) | swarm-worker §8.5 | projects §8.5 |
| `events.ingest` | IM | swarm-worker §8.1 | projects §8.7 |
| `nodesInternal.owned` | IQ (dead code) | projects §9.7 | swarm-worker §20 |
| `nodesInternal.listDeployable` | IQ | projects §9.7 | swarm-worker §8.5 |
| `nodesInternal.applyInput` | IQ | swarm-worker §6.1 | projects §7.2; observability §8.2 (`withTracing`) |
| `nodesInternal.setObserved` | IM | swarm-worker §8.7 | projects §7.3 |
| `nodesInternal.scheduleObserve` | IM (debounce) | swarm-worker §8.2 | projects §8.6 |
| `nodesInternal.clearObserveScheduled` | IM | swarm-worker §8.2 | projects §9.7 |
| `nodesInternal.setApplyError` | IM | swarm-worker §6.4 | projects §9.7 |
| `nodesInternal.followPort` | IM | proxy-ingress §5.6 | swarm-worker §6.5; projects §7.2 |
| `worker.config` | IQ (served as `GET /worker/config`) | observability §11.1 | swarm-worker §12 |

### 1.4 Log sinks, logs, traces, tracing, OTLP relay

| Function | Kind | Primary | Also |
| --- | --- | --- | --- |
| `logSinks.get` | P Q | observability §4.1 | web-data Q4/Q6, §5.7 |
| `logSinks.forNode` | IQ | observability §4.2 | auth-orgs §9.3 |
| `logSinks.forEnvironment` | IQ | observability §4.3 | auth-orgs §9.3 |
| `logSinks.save` | IM | observability §4.4 | — |
| `logSinks.connectAxiom` | P A (no UI caller) | observability §4.5 | auth-orgs §9.3 |
| `logSinks.disconnect` | P M | observability §4.6 | web-data M16 |
| `logSinks.beginAxiomSignIn` | P A | observability §4.7 | web-data A3; auth-orgs §9.3, §13 (defect 4) |
| `logSinks.clientFor` | IQ | observability §4.7 (step 2) | — |
| `logSinks.saveClient` | IM | observability §4.7 | — |
| `logSinks.startSignIn` | IM | observability §4.7 (step 5) | — |
| `logSinks.signInAxiom` | P A | observability §4.7 | web-data A10 |
| `logSinks.takeSignIn` | IM | observability §4.7 | — |
| `logSinks.stashPending` | IM | observability §4.7 | — |
| `logSinks.chooseAxiomOrg` | P A | observability §4.7 | web-data A4 |
| `logSinks.takePending` | IM | observability §4.7 | — |
| `logSinks.pendingOrgs` | P Q | observability §4.7 | web-data Q7/Q8 |
| `logSinks.cancelAxiomSignIn` | P M | observability §4.7 | web-data M17 |
| `logSinks.dropSignIn` | IM (S, +10 min) | observability §4.7 "Scheduled expiries" | — |
| `logSinks.dropPending` | IM (S, +10 min) | observability §4.7 "Scheduled expiries" | — |
| `logs.tail` | P A (polled 3 s) | observability §5.1 | web-data A1; cli-install A10.4, A11 |
| `logs.recent` | P A (polled 10 s) | observability §5.2 | web-data A5 |
| `logs.around` | P A | observability §5.3 | web-data A7 |
| `traces.overview` | P A (polled 10 s) | observability §6.3 | web-data A6; cli-install A10.5, A11 |
| `traces.get` | P A | observability §6.4 | web-data A9 |
| `traces.around` | P A | observability §6.5 | web-data A8 |
| `tracing.scope` | IQ | observability §8.3 | auth-orgs §9.3 |
| `tracing.setEnabled` | IM | observability §8.4 | projects §9.10 |
| `tracing.enable` | P A | observability §8.5 | web-data A2; cli-install A11 |
| `tracing.forNode` | P Q | observability §8.6 | web-data Q17, §5.5; cli-install A11 |
| `tracing.localEnv` | P A | observability §8.7 | cli-install A10.3 (`keel run`), A11 |
| `tracing.prompt` | P Q (`meta.realtime = false` in Go) | observability §8.8, §10 (text) | web-data Q20; cli-install A11 |
| `otlp.keyOf` | IQ | observability §9.1 | — |
| `otlp.saveKey` | IM | observability §9.1 | web-data §10.3 |
| `otlp.route` | IQ | observability §9.1 | — |
| `otlp.traces` | H (`POST /otlp/v1/traces`) | observability §9.2 | auth-orgs §10 |

### 1.5 keel-proxy

| Function | Kind | Primary | Also |
| --- | --- | --- | --- |
| `proxy.sync` | IA (Node runtime) | proxy-ingress §5.1, §6 (Caddy config) | swarm-worker §7 |
| `proxyInternal.syncInput` | IQ | proxy-ingress §5.2 | — |
| `proxyInternal.setStatuses` | IM | proxy-ingress §5.3 | — |
| `proxyInternal.resync` | IM (cron) | proxy-ingress §5.4 | swarm-worker §16.2 |
| `proxyInternal.certReport` | IM | proxy-ingress §5.5 | auth-orgs §10 |

### 1.6 HTTP routes, cron, scheduled jobs

| Entry point | Kind | Primary | Also |
| --- | --- | --- | --- |
| `POST /worker/events` → `events.ingest` | H, bearer `KEEL_WORKER_TOKEN` | swarm-worker §11.2 | auth-orgs §10; projects §8.7 |
| `GET /worker/config` → `worker.config` | H, bearer | swarm-worker §11.3 | observability §11.1; auth-orgs §10 |
| `POST /proxy/events` → `proxyInternal.certReport` | H, bearer | proxy-ingress §5.9 | auth-orgs §10; swarm-worker §11.4 |
| `POST /otlp/v1/traces` (`otlp.traces`) | H, environment ingest key | observability §9.2 | auth-orgs §10 |
| `GET /.well-known/openid-configuration` (redirect) | H (component) | auth-orgs §10 | dropped in Go |
| cron `keel-proxy resync` (every 2 min → `proxyInternal.resync`) | C | proxy-ingress §5.4 | swarm-worker §16.2 |
| `scheduler.runAfter` targets: `swarm.apply`, `reconcile.timeoutDeployment` (+5 min) from `beginDeployment`; `swarm.remove`, `reconcile.run`, `proxy.sync` from `nodes.remove`; `proxy.sync` from expose/unexpose/followPort/resync/migrations; `swarm.removeLegacyTunnels` from migrations; `swarm.observeNode` (debounced 500 ms / settle +2 s) from `scheduleObserveFor`; `swarm.observe` and `swarm.observeSwarmNodes` from `events.ingest`; `logSinks.dropSignIn` / `dropPending` (+10 min) | S | swarm-worker §7 (call graph), §18 (durability) | projects §0, §7; proxy-ingress §5.1; observability §4.7 |
| Pure helpers: `access.ts`, `status.ts`, `nodeHelpers.ts`, `endpoints.ts`, `timeRange.ts`, `tracingPrompt.ts`, `variables.ts` (`computeEnv`, `markReferrersDirty`, `renameReferences`, `serviceHost`), `tracing.ts` (`tracingEnv`, `withTracing`), `logProviders/*`, `traceProviders/*`, `otlp.ts` (`ensureKey`) | — | auth-orgs §9 / projects §2–§5 / swarm-worker §9 / proxy-ingress §3 / observability §3, §5–§8, §10 | — |

## 2. Tables → spec

| Table | Store | Primary | Also | Indexes |
| --- | --- | --- | --- | --- |
| `projects` | app | projects §1.1 | auth-orgs §3.2 (`organizationId`, legacy `ownerId`) | `by_organization [organizationId]`, `by_slug [organizationId, slug]` |
| `environments` | app | projects §1.2 | — | `by_project [projectId]` |
| `nodes` | app | projects §1.3 | swarm-worker §2.1; proxy-ingress §2.1–2.3 (`endpoints`, legacy `public`/`ingress`); observability §1.7 (`desired.tracing`) | `by_environment [environmentId]` |
| `variables` | app | projects §1.4 | proxy-ingress §2.4 | `by_node [nodeId]` |
| `deployments` | app | projects §1.5 | swarm-worker §2.2 | `by_environment [environmentId]`, `by_status [status]` |
| `cluster` | app (single row) | projects §1.6 | swarm-worker §2.3 | — |
| `logSinks` | app | observability §1.1, §1.8 (legacy rows) | auth-orgs §3.2 | `by_organization [organizationId]` |
| `axiomClients` | app | observability §1.2 | — | `by_redirect [redirectUri]` |
| `axiomSignIns` | app | observability §1.3 | auth-orgs §3.2 | `by_state [state]`, `by_organization [organizationId]` |
| `axiomPending` | app | observability §1.4 | auth-orgs §3.2 | `by_organization [organizationId]` |
| `otlpKeys` | app | observability §1.5 | — | `by_key [key]`, `by_environment [environmentId]` |
| `user` | Better Auth component | auth-orgs §3.1 | — | `email_name`, `name`, `userId` |
| `session` | Better Auth component | auth-orgs §3.1 | — | `expiresAt`, `expiresAt_userId`, `token`, `userId` |
| `account` | Better Auth component | auth-orgs §3.1 | — | `accountId`, `accountId_providerId`, `providerId_userId`, `userId` |
| `verification` | Better Auth component (not needed in Go) | auth-orgs §3.1 | — | `expiresAt`, `identifier` |
| `organization` | Better Auth component | auth-orgs §3.1 | observability §1.7 (slug → dataset names) | `name`, `slug` |
| `member` | Better Auth component | auth-orgs §3.1 | — | `organizationId`, `userId`, `role`, `organizationId_userId` |
| `invitation` | Better Auth component | auth-orgs §3.1 | — | `organizationId`, `email`, `role`, `status`, `inviterId`, `organizationId_status`, `email_organizationId_status` |
| `deviceCode` | Better Auth component | auth-orgs §3.1 | cli-install A8 | `deviceCode`, `userCode` |
| `jwks` | Better Auth component (not needed in Go) | auth-orgs §3.1 | — | — |
| `_scheduled_functions` | Convex system (via `nodes.observeScheduled`) | swarm-worker §8.2, §18 | projects §1.3 (do not port as a column) | — |

Embedded object types (schema validators): `desired`, `observed` → projects §1.3; `endpoint`,
`endpointStatus` → proxy-ingress §2.1; `position`, `config` → projects §1.3, §3.5; `deployStep`,
`logLine` → projects §1.5; `logSink`, `axiomOrg` → observability §1.6; `dockerEvent` →
swarm-worker §8.1; `engine` → projects §3.2; `timeRange` → observability §3.1.

Per-node agent state (not a table): `state.json` `{eventsSince?, logsSince}` → swarm-worker §13.7,
observability §11.3.
