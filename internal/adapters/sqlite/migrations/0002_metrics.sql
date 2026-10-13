-- Resource metrics the agents sample every 15 s (docs/metrics.md), kept 7 days.

-- One row per running task per sample. Replicas of a service on the same tick share `at`.
CREATE TABLE service_metrics (
  node_id           TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  task              TEXT NOT NULL,
  server_id         TEXT NOT NULL,
  at                INTEGER NOT NULL,
  cpu_cores         REAL NOT NULL,
  memory_bytes      INTEGER NOT NULL,
  rx_bytes_per_sec  REAL NOT NULL,
  tx_bytes_per_sec  REAL NOT NULL,
  PRIMARY KEY (node_id, at, task)
) WITHOUT ROWID;
CREATE INDEX service_metrics_by_time ON service_metrics(at);

-- One row per Swarm node (a server) per sample: host totals, not only Keel's tasks.
CREATE TABLE server_metrics (
  server_id          TEXT NOT NULL,
  name               TEXT NOT NULL,
  at                 INTEGER NOT NULL,
  cpus               INTEGER NOT NULL,
  cpu_cores          REAL NOT NULL,
  memory_bytes       INTEGER NOT NULL,
  memory_total_bytes INTEGER NOT NULL,
  disk_bytes         INTEGER NOT NULL,
  disk_total_bytes   INTEGER NOT NULL,
  PRIMARY KEY (server_id, at)
) WITHOUT ROWID;
CREATE INDEX server_metrics_by_time ON server_metrics(at);

-- The Observability page's panels of an environment, in order. No row = the default layout.
CREATE TABLE dashboards (
  environment_id TEXT PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
  panels         TEXT NOT NULL,
  updated_at     INTEGER NOT NULL
);
