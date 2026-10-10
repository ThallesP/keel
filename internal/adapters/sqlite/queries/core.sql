-- Shared reads every area uses. Area files are named queries/<area>.sql and prefix their query
-- names with the area (AuthGetUser, CanvasInsertNode, DeployListRunning, ...) so that sqlc's one
-- generated package never sees a duplicate.
-- ASCII only in this directory: sqlc miscounts offsets after multi-byte characters.

-- name: CoreGetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: CoreGetEnvironment :one
SELECT * FROM environments WHERE id = ?;

-- name: CoreGetNode :one
SELECT * FROM nodes WHERE id = ?;

-- name: CoreListNodesByEnvironment :many
SELECT * FROM nodes WHERE environment_id = ? ORDER BY created_at, id;

-- name: CoreListAllNodes :many
SELECT * FROM nodes ORDER BY created_at, id;

-- name: CoreListEndpointsByNode :many
SELECT * FROM endpoints WHERE node_id = ? ORDER BY ord;

-- name: CoreListEndpointsByEnvironment :many
SELECT e.* FROM endpoints e JOIN nodes n ON n.id = e.node_id
WHERE n.environment_id = ? ORDER BY e.node_id, e.ord;

-- name: CoreListAllEndpoints :many
SELECT * FROM endpoints ORDER BY node_id, ord;

-- name: CoreOrganizationOfEnvironment :one
SELECT p.organization_id FROM environments e JOIN projects p ON p.id = e.project_id
WHERE e.id = ?;

-- name: CoreGetSetting :one
SELECT value FROM settings WHERE key = ?;

-- name: CoreSetSetting :exec
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: CoreInsertNode :exec
INSERT INTO nodes (
  id, environment_id, type, name, parent_id, position_x, position_y,
  config_size_gb, config_width, config_height,
  desired_image, desired_revision, desired_replicas, desired_port, desired_tracing,
  observed_revision, observed_running, observed_completed, observed_finished_at, observed_state,
  observed_node_ids, observed_error, observed_at,
  deployed_revision, dirty, shipped_at, apply_error, one_shot, created_at
) VALUES (
  ?, ?, ?, ?, ?, ?, ?,
  ?, ?, ?,
  ?, ?, ?, ?, ?,
  ?, ?, ?, ?, ?,
  ?, ?, ?,
  ?, ?, ?, ?, ?, ?
);

-- name: CoreUpdateNode :execrows
UPDATE nodes SET
  environment_id = ?2, type = ?3, name = ?4, parent_id = ?5, position_x = ?6, position_y = ?7,
  config_size_gb = ?8, config_width = ?9, config_height = ?10,
  desired_image = ?11, desired_revision = ?12, desired_replicas = ?13, desired_port = ?14,
  desired_tracing = ?15,
  observed_revision = ?16, observed_running = ?17, observed_completed = ?18,
  observed_finished_at = ?19, observed_state = ?20, observed_node_ids = ?21, observed_error = ?22,
  observed_at = ?23,
  deployed_revision = ?24, dirty = ?25, shipped_at = ?26, apply_error = ?27, one_shot = ?28,
  created_at = ?29
WHERE id = ?1;

-- name: CoreDeleteNode :exec
DELETE FROM nodes WHERE id = ?;

-- name: CoreDeleteEndpointsOfNode :exec
DELETE FROM endpoints WHERE node_id = ?;

-- name: CoreInsertEndpoint :exec
INSERT INTO endpoints (
  id, node_id, ord, protocol, port, pinned_port, domain, public_port,
  status_state, status_error, status_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
