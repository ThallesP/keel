-- Keel's control-plane database.
-- Times are unix milliseconds. Booleans are INTEGER 0/1. Ids are domain.NewID() strings.

-- ── Accounts ────────────────────────────────────────────────────────────────────────────────

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name          TEXT NOT NULL DEFAULT '',
  -- argon2id PHC string ($argon2id$v=19$m=…,t=…,p=…$salt$hash).
  password_hash TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- A signed-in browser or CLI. Only the SHA-256 of the token is stored.
CREATE TABLE sessions (
  id         TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  ip         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_by_user ON sessions(user_id);

-- One per install for now, founded by the first account when it signs up.
CREATE TABLE organizations (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  slug       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);

CREATE TABLE members (
  id              TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id         TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role            TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member')),
  created_at      INTEGER NOT NULL,
  UNIQUE (organization_id, user_id)
);
CREATE INDEX members_by_user ON members(user_id);

-- The id is the secret in the invite link.
CREATE TABLE invitations (
  id              TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email           TEXT NOT NULL COLLATE NOCASE,
  role            TEXT NOT NULL DEFAULT 'member',
  status          TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'canceled')),
  inviter_id      TEXT NOT NULL DEFAULT '',
  expires_at      INTEGER NOT NULL,
  created_at      INTEGER NOT NULL
);
CREATE INDEX invitations_by_organization ON invitations(organization_id, status);

-- `keel login` (RFC 8628). device_code_hash: SHA-256 of the secret the CLI polls with.
CREATE TABLE device_codes (
  id               TEXT PRIMARY KEY,
  device_code_hash TEXT NOT NULL UNIQUE,
  user_code        TEXT NOT NULL UNIQUE,
  status           TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied')),
  user_id          TEXT REFERENCES users(id) ON DELETE CASCADE,
  last_polled_at   INTEGER NOT NULL DEFAULT 0,
  expires_at       INTEGER NOT NULL,
  created_at       INTEGER NOT NULL
);

-- ── Projects and the canvas ─────────────────────────────────────────────────────────────────

CREATE TABLE projects (
  id              TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name            TEXT NOT NULL,
  slug            TEXT NOT NULL,
  created_at      INTEGER NOT NULL,
  UNIQUE (organization_id, slug)
);

CREATE TABLE environments (
  id            TEXT PRIMARY KEY,
  project_id    TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  is_production INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
CREATE INDEX environments_by_project ON environments(project_id);

-- A canvas node. `desired` is present iff desired_image IS NOT NULL; `observed` iff observed_at
-- IS NOT NULL (domain.Node). volume and group never have either.
CREATE TABLE nodes (
  id                   TEXT PRIMARY KEY,
  environment_id       TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  type                 TEXT NOT NULL CHECK (type IN ('service', 'database', 'cache', 'volume', 'group')),
  name                 TEXT NOT NULL,
  parent_id            TEXT REFERENCES nodes(id) ON DELETE SET NULL,
  position_x           REAL NOT NULL DEFAULT 0,
  position_y           REAL NOT NULL DEFAULT 0,
  config_size_gb       REAL,
  config_width         REAL,
  config_height        REAL,
  desired_image        TEXT,
  desired_revision     INTEGER NOT NULL DEFAULT 0,
  desired_replicas     INTEGER NOT NULL DEFAULT 0,
  desired_port         INTEGER NOT NULL DEFAULT 0,
  desired_tracing      INTEGER NOT NULL DEFAULT 0,
  observed_revision    INTEGER NOT NULL DEFAULT 0,
  observed_running     INTEGER NOT NULL DEFAULT 0,
  observed_completed   INTEGER NOT NULL DEFAULT 0,
  observed_finished_at INTEGER NOT NULL DEFAULT 0,
  observed_state       TEXT,
  observed_error       TEXT,
  observed_at          INTEGER,
  deployed_revision    INTEGER NOT NULL DEFAULT 0,
  dirty                INTEGER NOT NULL DEFAULT 0,
  shipped_at           INTEGER,
  apply_error          TEXT,
  one_shot             INTEGER NOT NULL DEFAULT 0,
  created_at           INTEGER NOT NULL
);
CREATE INDEX nodes_by_environment ON nodes(environment_id);
-- Node names are unique per environment: `${{ name.KEY }}` references resolve by name
-- (docs/go/spec/projects.md section 0). A violation surfaces as app.ErrCanvasTaken.
CREATE UNIQUE INDEX nodes_environment_name ON nodes(environment_id, name);

-- A node's public endpoints. Domains and public ports are unique across the install.
CREATE TABLE endpoints (
  id           TEXT PRIMARY KEY,
  node_id      TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  ord          INTEGER NOT NULL, -- order within the node
  protocol     TEXT NOT NULL CHECK (protocol IN ('http', 'tcp', 'udp')),
  port         INTEGER NOT NULL,
  pinned_port  INTEGER NOT NULL DEFAULT 0,
  domain       TEXT NOT NULL DEFAULT '',
  public_port  INTEGER NOT NULL DEFAULT 0,
  status_state TEXT NOT NULL CHECK (status_state IN ('starting', 'live', 'failed')),
  status_error TEXT NOT NULL DEFAULT '',
  status_at    INTEGER NOT NULL
);
CREATE INDEX endpoints_by_node ON endpoints(node_id, ord);
CREATE UNIQUE INDEX endpoints_domain ON endpoints(domain) WHERE domain <> '';
CREATE UNIQUE INDEX endpoints_public_port ON endpoints(protocol, public_port) WHERE public_port <> 0;

CREATE TABLE variables (
  id      TEXT PRIMARY KEY,
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  key     TEXT NOT NULL,
  value   TEXT NOT NULL,
  secret  INTEGER NOT NULL DEFAULT 0,
  UNIQUE (node_id, key)
);

-- ── Deployments ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE deployments (
  id             TEXT PRIMARY KEY,
  environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
  message        TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('running', 'success', 'failed')),
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER
);
CREATE INDEX deployments_by_environment ON deployments(environment_id, started_at);
CREATE INDEX deployments_by_status ON deployments(status);

-- deployments.steps. node_id has no foreign key: a step outlives a deleted node.
CREATE TABLE deployment_steps (
  deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  idx           INTEGER NOT NULL,
  node_id       TEXT,
  label         TEXT NOT NULL,
  status        TEXT NOT NULL CHECK (status IN ('pending', 'running', 'done', 'failed')),
  started_at    INTEGER,
  applied_at    INTEGER,
  finished_at   INTEGER,
  PRIMARY KEY (deployment_id, idx)
);

-- deployments.log
CREATE TABLE deployment_log (
  seq           INTEGER PRIMARY KEY AUTOINCREMENT,
  deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
  at            INTEGER NOT NULL,
  node_id       TEXT,
  text          TEXT NOT NULL
);
CREATE INDEX deployment_log_by_deployment ON deployment_log(deployment_id, seq);

-- Single row: ready Swarm nodes.
CREATE TABLE cluster (
  id      INTEGER PRIMARY KEY CHECK (id = 1),
  servers INTEGER NOT NULL,
  at      INTEGER NOT NULL
);

-- ── Observability ───────────────────────────────────────────────────────────────────────────

-- At most one per organization (docs/logs.md). token is a secret: it leaves the server only to
-- the agent (bearer-protected /worker/config).
CREATE TABLE log_sinks (
  id              TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL UNIQUE REFERENCES organizations(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL CHECK (kind IN ('axiom')),
  domain          TEXT NOT NULL,
  dataset         TEXT NOT NULL,
  traces          TEXT,
  token           TEXT NOT NULL,
  org             TEXT,
  created_at      INTEGER NOT NULL
);

-- Sign in with Axiom: the OAuth client this install registered (DCR), one per callback URL.
CREATE TABLE axiom_clients (
  redirect_uri TEXT PRIMARY KEY,
  client_id    TEXT NOT NULL,
  created_at   INTEGER NOT NULL
);

-- Sign in with Axiom, between begin and Axiom's redirect back. Single use, 10 minutes.
CREATE TABLE axiom_sign_ins (
  state           TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  client_id       TEXT NOT NULL,
  verifier        TEXT NOT NULL,
  redirect_uri    TEXT NOT NULL,
  created_at      INTEGER NOT NULL
);
CREATE INDEX axiom_sign_ins_by_organization ON axiom_sign_ins(organization_id);

-- Sign in with Axiom, between the code exchange and the user picking one of several orgs.
CREATE TABLE axiom_pending (
  organization_id TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  token           TEXT NOT NULL,
  orgs            TEXT NOT NULL, -- JSON array of {id, name, domain, maxDatasets?}
  created_at      INTEGER NOT NULL
);

-- The OTLP relay's ingest key of an environment. At most one per environment.
CREATE TABLE otlp_keys (
  environment_id TEXT PRIMARY KEY REFERENCES environments(id) ON DELETE CASCADE,
  key            TEXT NOT NULL UNIQUE,
  created_at     INTEGER NOT NULL
);
