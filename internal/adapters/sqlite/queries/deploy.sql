-- Deployments, their steps and log, and the cluster row. Owner: the deploy area.
-- "Newest" is creation order: started_at, then rowid (insertion order) for ties.

-- name: DeployHasRunning :one
SELECT COUNT(*) FROM deployments WHERE environment_id = ? AND status = 'running';

-- name: DeployInsert :exec
INSERT INTO deployments (id, environment_id, sha, message, status, started_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: DeployUpdate :execrows
UPDATE deployments SET status = ?2, finished_at = ?3 WHERE id = ?1;

-- name: DeployGet :one
SELECT * FROM deployments WHERE id = ?;

-- name: DeployLatest :one
SELECT * FROM deployments WHERE environment_id = ?
ORDER BY started_at DESC, rowid DESC LIMIT 1;

-- name: DeployListRecent :many
SELECT * FROM deployments WHERE environment_id = ?
ORDER BY started_at DESC, rowid DESC LIMIT ?;

-- name: DeployListRunning :many
SELECT * FROM deployments
WHERE status = 'running' AND (environment_id = sqlc.arg(environment_id) OR sqlc.arg(environment_id) = '')
ORDER BY started_at, rowid;

-- name: DeployInsertStep :exec
INSERT INTO deployment_steps (deployment_id, idx, node_id, label, status, started_at, applied_at, finished_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeployDeleteSteps :exec
DELETE FROM deployment_steps WHERE deployment_id = ?;

-- name: DeployListSteps :many
SELECT * FROM deployment_steps WHERE deployment_id = ? ORDER BY idx;

-- name: DeployInsertLog :exec
INSERT INTO deployment_log (deployment_id, at, node_id, text) VALUES (?, ?, ?, ?);

-- name: DeployListLog :many
SELECT * FROM deployment_log WHERE deployment_id = ? ORDER BY seq;

-- name: DeployTrimLog :exec
DELETE FROM deployment_log
WHERE deployment_log.deployment_id = ?1 AND deployment_log.seq <= (
  SELECT l.seq FROM deployment_log AS l WHERE l.deployment_id = ?1
  ORDER BY l.seq DESC LIMIT 1 OFFSET ?2
);

-- name: DeployGetCluster :one
SELECT servers FROM cluster WHERE id = 1;

-- name: DeploySetCluster :exec
INSERT INTO cluster (id, servers, at) VALUES (1, ?, ?)
ON CONFLICT (id) DO UPDATE SET servers = excluded.servers, at = excluded.at;

-- name: DeployListOrganizationIDs :many
SELECT id FROM organizations ORDER BY created_at, id;
