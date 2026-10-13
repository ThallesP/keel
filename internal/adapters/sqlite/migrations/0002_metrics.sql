-- Resource metrics the agents sample every 15 s (docs/metrics.md): raw samples for the last
-- hours, hourly rollups for 7 days.

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

-- Per service per hour: replicas summed per tick, then the hour's average CPU and network and
-- peak memory. The 24h and 7d ranges read these instead of raw samples.
CREATE TABLE service_metrics_hourly (
  node_id          TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  hour             INTEGER NOT NULL,
  cpu_cores        REAL NOT NULL,
  memory_bytes     REAL NOT NULL,
  rx_bytes_per_sec REAL NOT NULL,
  tx_bytes_per_sec REAL NOT NULL,
  PRIMARY KEY (node_id, hour)
) WITHOUT ROWID;
CREATE INDEX service_metrics_hourly_by_time ON service_metrics_hourly(hour);

-- Per environment per hour: every service summed per tick first, so a service that ran part of
-- the hour counts only for the ticks it ran. The 24h and 7d totals read these.
CREATE TABLE environment_metrics_hourly (
  environment_id   TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  hour             INTEGER NOT NULL,
  cpu_cores        REAL NOT NULL,
  memory_bytes     REAL NOT NULL,
  rx_bytes_per_sec REAL NOT NULL,
  tx_bytes_per_sec REAL NOT NULL,
  PRIMARY KEY (environment_id, hour)
) WITHOUT ROWID;
CREATE INDEX environment_metrics_hourly_by_time ON environment_metrics_hourly(hour);

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
