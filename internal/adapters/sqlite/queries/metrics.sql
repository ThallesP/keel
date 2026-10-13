-- Metrics: resource samples from the agents and the Observability page's panels
-- (docs/metrics.md). ASCII only.

-- name: MetricsNodeExists :one
SELECT COUNT(*) FROM nodes WHERE id = ?;

-- name: MetricsInsertService :exec
INSERT INTO service_metrics (node_id, task, server_id, at, cpu_cores, memory_bytes, rx_bytes_per_sec, tx_bytes_per_sec)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (node_id, at, task) DO NOTHING;

-- name: MetricsInsertServer :exec
INSERT INTO server_metrics (server_id, name, at, cpus, cpu_cores, memory_bytes, memory_total_bytes, disk_bytes, disk_total_bytes)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (server_id, at) DO NOTHING;

-- name: MetricsServiceBuckets :many
SELECT
  node_id,
  CAST(at / sqlc.arg(bin_ms) AS INTEGER) * sqlc.arg(bin_ms) AS bucket,
  CAST(AVG(cpu_cores) AS REAL) AS cpu_cores,
  CAST(MAX(memory_bytes) AS REAL) AS memory_bytes,
  CAST(AVG(rx_bytes_per_sec) AS REAL) AS rx_bytes_per_sec,
  CAST(AVG(tx_bytes_per_sec) AS REAL) AS tx_bytes_per_sec
FROM (
  SELECT
    m.node_id AS node_id,
    m.at AS at,
    SUM(m.cpu_cores) AS cpu_cores,
    SUM(m.memory_bytes) AS memory_bytes,
    SUM(m.rx_bytes_per_sec) AS rx_bytes_per_sec,
    SUM(m.tx_bytes_per_sec) AS tx_bytes_per_sec
  FROM service_metrics m
  JOIN nodes n ON n.id = m.node_id
  WHERE n.environment_id = sqlc.arg(environment_id)
    AND (CAST(sqlc.arg(node_id) AS TEXT) = '' OR m.node_id = sqlc.arg(node_id))
    AND m.at >= sqlc.arg(from_ms) AND m.at < sqlc.arg(to_ms)
  GROUP BY m.node_id, m.at
)
GROUP BY node_id, bucket
ORDER BY node_id, bucket;

-- name: MetricsTotalBuckets :many
SELECT
  CAST(at / sqlc.arg(bin_ms) AS INTEGER) * sqlc.arg(bin_ms) AS bucket,
  CAST(AVG(cpu_cores) AS REAL) AS cpu_cores,
  CAST(MAX(memory_bytes) AS REAL) AS memory_bytes,
  CAST(AVG(rx_bytes_per_sec) AS REAL) AS rx_bytes_per_sec,
  CAST(AVG(tx_bytes_per_sec) AS REAL) AS tx_bytes_per_sec
FROM (
  SELECT
    m.at AS at,
    SUM(m.cpu_cores) AS cpu_cores,
    SUM(m.memory_bytes) AS memory_bytes,
    SUM(m.rx_bytes_per_sec) AS rx_bytes_per_sec,
    SUM(m.tx_bytes_per_sec) AS tx_bytes_per_sec
  FROM service_metrics m
  JOIN nodes n ON n.id = m.node_id
  WHERE n.environment_id = sqlc.arg(environment_id)
    AND (CAST(sqlc.arg(node_id) AS TEXT) = '' OR m.node_id = sqlc.arg(node_id))
    AND m.at >= sqlc.arg(from_ms) AND m.at < sqlc.arg(to_ms)
  GROUP BY m.at
)
GROUP BY bucket
ORDER BY bucket;

-- name: MetricsRecentPlacements :many
SELECT DISTINCT m.server_id, m.node_id
FROM service_metrics m
JOIN nodes n ON n.id = m.node_id
WHERE n.environment_id = ? AND m.at >= ?
ORDER BY m.server_id, m.node_id;

-- name: MetricsLatestServer :one
SELECT * FROM server_metrics WHERE server_id = ? ORDER BY at DESC LIMIT 1;

-- name: MetricsListDeployments :many
SELECT id, message, status, started_at FROM deployments
WHERE environment_id = sqlc.arg(environment_id) AND started_at >= sqlc.arg(from_ms) AND started_at < sqlc.arg(to_ms)
ORDER BY started_at;

-- name: MetricsPruneServices :exec
DELETE FROM service_metrics WHERE at < ?;

-- name: MetricsPruneServers :exec
DELETE FROM server_metrics WHERE at < ?;

-- name: MetricsGetDashboard :one
SELECT panels FROM dashboards WHERE environment_id = ?;

-- name: MetricsUpsertDashboard :exec
INSERT INTO dashboards (environment_id, panels, updated_at) VALUES (?, ?, ?)
ON CONFLICT (environment_id) DO UPDATE SET panels = excluded.panels, updated_at = excluded.updated_at;
