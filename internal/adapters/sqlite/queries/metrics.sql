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

-- name: MetricsRollupHours :exec
INSERT INTO service_metrics_hourly (node_id, hour, cpu_cores, memory_bytes, rx_bytes_per_sec, tx_bytes_per_sec)
SELECT
  node_id,
  CAST(at / 3600000 AS INTEGER) * 3600000 AS hour,
  CAST(AVG(cpu_cores) AS REAL),
  CAST(MAX(memory_bytes) AS REAL),
  CAST(AVG(rx_bytes_per_sec) AS REAL),
  CAST(AVG(tx_bytes_per_sec) AS REAL)
FROM (
  SELECT
    node_id,
    at,
    SUM(cpu_cores) AS cpu_cores,
    SUM(memory_bytes) AS memory_bytes,
    SUM(rx_bytes_per_sec) AS rx_bytes_per_sec,
    SUM(tx_bytes_per_sec) AS tx_bytes_per_sec
  FROM service_metrics
  WHERE at >= sqlc.arg(since_ms)
  GROUP BY node_id, at
)
WHERE true
GROUP BY node_id, hour
ON CONFLICT (node_id, hour) DO UPDATE SET
  cpu_cores = excluded.cpu_cores, memory_bytes = excluded.memory_bytes,
  rx_bytes_per_sec = excluded.rx_bytes_per_sec, tx_bytes_per_sec = excluded.tx_bytes_per_sec;

-- name: MetricsServiceBucketsHourly :many
SELECT
  h.node_id,
  CAST(h.hour / sqlc.arg(bin_ms) AS INTEGER) * sqlc.arg(bin_ms) AS bucket,
  CAST(AVG(h.cpu_cores) AS REAL) AS cpu_cores,
  CAST(MAX(h.memory_bytes) AS REAL) AS memory_bytes,
  CAST(AVG(h.rx_bytes_per_sec) AS REAL) AS rx_bytes_per_sec,
  CAST(AVG(h.tx_bytes_per_sec) AS REAL) AS tx_bytes_per_sec
FROM service_metrics_hourly h
JOIN nodes n ON n.id = h.node_id
WHERE n.environment_id = sqlc.arg(environment_id)
  AND (CAST(sqlc.arg(node_id) AS TEXT) = '' OR h.node_id = sqlc.arg(node_id))
  AND h.hour >= sqlc.arg(from_ms) AND h.hour < sqlc.arg(to_ms)
GROUP BY h.node_id, bucket
ORDER BY h.node_id, bucket;

-- name: MetricsRollupEnvironmentHours :exec
INSERT INTO environment_metrics_hourly (environment_id, hour, cpu_cores, memory_bytes, rx_bytes_per_sec, tx_bytes_per_sec)
SELECT
  environment_id,
  CAST(at / 3600000 AS INTEGER) * 3600000 AS hour,
  CAST(AVG(cpu_cores) AS REAL),
  CAST(MAX(memory_bytes) AS REAL),
  CAST(AVG(rx_bytes_per_sec) AS REAL),
  CAST(AVG(tx_bytes_per_sec) AS REAL)
FROM (
  SELECT
    n.environment_id AS environment_id,
    m.at AS at,
    SUM(m.cpu_cores) AS cpu_cores,
    SUM(m.memory_bytes) AS memory_bytes,
    SUM(m.rx_bytes_per_sec) AS rx_bytes_per_sec,
    SUM(m.tx_bytes_per_sec) AS tx_bytes_per_sec
  FROM service_metrics m
  JOIN nodes n ON n.id = m.node_id
  WHERE m.at >= sqlc.arg(since_ms)
  GROUP BY n.environment_id, m.at
)
WHERE true
GROUP BY environment_id, hour
ON CONFLICT (environment_id, hour) DO UPDATE SET
  cpu_cores = excluded.cpu_cores, memory_bytes = excluded.memory_bytes,
  rx_bytes_per_sec = excluded.rx_bytes_per_sec, tx_bytes_per_sec = excluded.tx_bytes_per_sec;

-- name: MetricsTotalBucketsHourly :many
SELECT
  CAST(hour / sqlc.arg(bin_ms) AS INTEGER) * sqlc.arg(bin_ms) AS bucket,
  CAST(AVG(cpu_cores) AS REAL) AS cpu_cores,
  CAST(MAX(memory_bytes) AS REAL) AS memory_bytes,
  CAST(AVG(rx_bytes_per_sec) AS REAL) AS rx_bytes_per_sec,
  CAST(AVG(tx_bytes_per_sec) AS REAL) AS tx_bytes_per_sec
FROM environment_metrics_hourly
WHERE environment_id = sqlc.arg(environment_id)
  AND hour >= sqlc.arg(from_ms) AND hour < sqlc.arg(to_ms)
GROUP BY bucket
ORDER BY bucket;

-- name: MetricsRecentPlacements :many
SELECT DISTINCT m.server_id, m.node_id
FROM service_metrics m
JOIN nodes n ON n.id = m.node_id
WHERE n.environment_id = ? AND m.at >= ?
ORDER BY m.server_id, m.node_id;

-- name: MetricsLatestServer :one
SELECT * FROM server_metrics WHERE server_id = ? AND at >= ? ORDER BY at DESC LIMIT 1;

-- name: MetricsListDeployments :many
SELECT id, message, status, started_at FROM deployments
WHERE environment_id = sqlc.arg(environment_id) AND started_at >= sqlc.arg(from_ms) AND started_at < sqlc.arg(to_ms)
ORDER BY started_at;

-- name: MetricsPruneServices :exec
DELETE FROM service_metrics WHERE at < ?;

-- name: MetricsPruneServers :exec
DELETE FROM server_metrics WHERE at < ?;

-- name: MetricsPruneHourly :exec
DELETE FROM service_metrics_hourly WHERE hour < ?;

-- name: MetricsPruneEnvironmentHourly :exec
DELETE FROM environment_metrics_hourly WHERE hour < ?;

-- name: MetricsGetDashboard :one
SELECT panels FROM dashboards WHERE environment_id = ?;

-- name: MetricsUpsertDashboard :exec
INSERT INTO dashboards (environment_id, panels, updated_at) VALUES (?, ?, ?)
ON CONFLICT (environment_id) DO UPDATE SET panels = excluded.panels, updated_at = excluded.updated_at;
