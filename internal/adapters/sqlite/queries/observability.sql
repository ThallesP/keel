-- Observability: log sinks, Sign in with Axiom state, OTLP ingest keys
-- (docs/go/spec/observability.md). ASCII only.

-- name: ObsGetSink :one
SELECT * FROM log_sinks WHERE organization_id = ?;

-- name: ObsDeleteSink :exec
DELETE FROM log_sinks WHERE organization_id = ?;

-- name: ObsInsertSink :exec
INSERT INTO log_sinks (id, organization_id, kind, domain, dataset, traces, token, org, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ObsCountSinks :one
SELECT COUNT(*) FROM log_sinks;

-- name: ObsListProjects :many
SELECT id, organization_id FROM projects ORDER BY created_at, id;

-- name: ObsListDesiredNodes :many
SELECT n.id, e.project_id FROM nodes n JOIN environments e ON e.id = n.environment_id
WHERE n.desired_image IS NOT NULL
ORDER BY n.created_at, n.id;

-- name: ObsGetOrganizationSlug :one
SELECT slug FROM organizations WHERE id = ?;

-- name: ObsGetAxiomClient :one
SELECT client_id FROM axiom_clients WHERE redirect_uri = ?;

-- name: ObsInsertAxiomClient :exec
INSERT INTO axiom_clients (redirect_uri, client_id, created_at) VALUES (?, ?, ?)
ON CONFLICT (redirect_uri) DO NOTHING;

-- name: ObsDeleteSignInsOfOrganization :exec
DELETE FROM axiom_sign_ins WHERE organization_id = ?;

-- name: ObsInsertSignIn :exec
INSERT INTO axiom_sign_ins (state, organization_id, client_id, verifier, redirect_uri, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ObsGetSignIn :one
SELECT * FROM axiom_sign_ins WHERE state = ?;

-- name: ObsDeleteSignIn :exec
DELETE FROM axiom_sign_ins WHERE state = ?;

-- name: ObsPurgeSignIns :exec
DELETE FROM axiom_sign_ins WHERE created_at <= ?;

-- name: ObsGetPending :one
SELECT * FROM axiom_pending WHERE organization_id = ?;

-- name: ObsUpsertPending :exec
INSERT INTO axiom_pending (organization_id, token, orgs, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (organization_id) DO UPDATE SET
  token = excluded.token, orgs = excluded.orgs, created_at = excluded.created_at;

-- name: ObsDeletePending :exec
DELETE FROM axiom_pending WHERE organization_id = ?;

-- name: ObsPurgePending :many
DELETE FROM axiom_pending WHERE created_at <= ? RETURNING organization_id;

-- name: ObsGetOTLPKey :one
SELECT key FROM otlp_keys WHERE environment_id = ?;

-- name: ObsInsertOTLPKey :exec
INSERT INTO otlp_keys (environment_id, key, created_at) VALUES (?, ?, ?);

-- name: ObsGetOTLPKeyEnvironment :one
SELECT environment_id FROM otlp_keys WHERE key = ?;

-- name: ObsListVariableKeys :many
SELECT key FROM variables WHERE node_id = ? ORDER BY rowid;
