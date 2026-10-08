-- Canvas area: projects, environments, variables, the cluster row it reads.
-- Creation order is rowid order (insertion order; an UPDATE keeps the rowid).

-- name: CanvasCountOrganizations :one
SELECT COUNT(*) FROM organizations;

-- name: CanvasListProjects :many
SELECT * FROM projects WHERE organization_id = ? ORDER BY rowid;

-- name: CanvasGetProjectBySlug :one
SELECT * FROM projects WHERE organization_id = ? AND slug = ?;

-- name: CanvasInsertProject :exec
INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES (?, ?, ?, ?, ?);

-- name: CanvasListEnvironments :many
SELECT * FROM environments WHERE project_id = ? ORDER BY rowid;

-- name: CanvasInsertEnvironment :exec
INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES (?, ?, ?, ?, ?);

-- name: CanvasGetClusterServers :many
SELECT servers FROM cluster WHERE id = 1;

-- name: CanvasMarkDirty :exec
UPDATE nodes SET dirty = 1 WHERE id = ?;

-- name: CanvasListVariables :many
SELECT * FROM variables WHERE node_id = ? ORDER BY rowid;

-- name: CanvasListEnvironmentVariables :many
SELECT v.* FROM variables v JOIN nodes n ON n.id = v.node_id
WHERE n.environment_id = ? ORDER BY v.rowid;

-- name: CanvasInsertVariable :exec
INSERT INTO variables (id, node_id, key, value, secret) VALUES (?, ?, ?, ?, ?);

-- name: CanvasUpdateVariable :execrows
UPDATE variables SET key = ?2, value = ?3, secret = ?4 WHERE id = ?1;

-- name: CanvasDeleteVariable :exec
DELETE FROM variables WHERE id = ?;

-- name: CanvasDeleteNodeVariables :exec
DELETE FROM variables WHERE node_id = ?;
