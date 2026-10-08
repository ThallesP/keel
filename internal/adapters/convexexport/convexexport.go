// Package convexexport imports a Convex snapshot export (`npx convex export`, or a dashboard
// backup) of a Keel install into a fresh SQLite database, so an install upgraded from the Convex
// control plane keeps its accounts, projects, canvas, variables, deployments and log sink.
//
// Layout: every table is a `<table>/documents.jsonl` (one JSON document per line). App tables sit
// at the root; Better Auth's tables live in the betterAuth component's folder. The importer finds
// each table by its folder name anywhere in the archive, so the component path does not matter.
//
// Ids are kept verbatim: node ids appear in Swarm service names (svc-<id>), labels, default
// domains and OTel attributes. Not imported: Better Auth's verification/jwks/deviceCode rows,
// Sign in with Axiom's transient rows, and Convex's scheduled functions (serve's start-up pass
// re-observes everything).
package convexexport

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Report is what Import did, per table.
type Report struct {
	Imported map[string]int `json:"imported"`
	Skipped  map[string]int `json:"skipped"`
	Warnings []string       `json:"warnings"`
}

func (r *Report) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// HashToken turns a Better Auth session token into what the sessions table stores. serve sets it
// to the auth area's hash so imported CLI sessions keep working.
var HashToken func(token string) string

type doc = map[string]any

// tables reads every <table>/documents.jsonl under root (a directory or a .zip).
func tables(src string) (map[string][]doc, error) {
	out := map[string][]doc{}
	add := func(name string, r io.Reader) error {
		table := path.Base(path.Dir(name))
		if _, ok := out[table]; !ok {
			out[table] = []doc{} // an empty table still says what the export is
		}
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var d doc
			if err := json.Unmarshal([]byte(line), &d); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			out[table] = append(out[table], d)
		}
		return sc.Err()
	}
	info, err := os.Stat(src)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "documents.jsonl" {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			rel, _ := filepath.Rel(src, p)
			return add(filepath.ToSlash(rel), f)
		})
		sortByCreation(out)
		return out, err
	}
	z, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	for _, f := range z.File {
		if path.Base(f.Name) != "documents.jsonl" {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = add(f.Name, r)
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	sortByCreation(out)
	return out, nil
}

// sortByCreation orders every table by _creationTime: Keel orders variables (the container's
// env order), projects and environments by insertion, as Convex did by creation.
func sortByCreation(t map[string][]doc) {
	for _, docs := range t {
		sort.SliceStable(docs, func(i, j int) bool {
			a, _ := num(docs[i], "_creationTime")
			b, _ := num(docs[j], "_creationTime")
			return a < b
		})
	}
}

// Value helpers: Convex numbers are JSON numbers (float64); optional fields may be absent or null.

func s(d doc, k string) string {
	v, _ := d[k].(string)
	return v
}

func ns(d doc, k string) any { // nullable string
	if v, ok := d[k].(string); ok && v != "" {
		return v
	}
	return nil
}

func num(d doc, k string) (float64, bool) {
	switch v := d[k].(type) {
	case float64:
		return v, true
	case map[string]any: // {"$integer": "..."} would be an Int64; Keel never stores one
		return 0, false
	}
	return 0, false
}

func i64(d doc, k string) int64 {
	v, _ := num(d, k)
	return int64(math.Round(v))
}

func ni64(d doc, k string) any { // nullable integer
	if v, ok := num(d, k); ok {
		return int64(math.Round(v))
	}
	return nil
}

func nf(d doc, k string) any { // nullable float
	if v, ok := num(d, k); ok {
		return v
	}
	return nil
}

func b(d doc, k string) int64 {
	if v, _ := d[k].(bool); v {
		return 1
	}
	return 0
}

func obj(d doc, k string) doc {
	v, _ := d[k].(map[string]any)
	return v
}

func arr(d doc, k string) []any {
	v, _ := d[k].([]any)
	return v
}

func created(d doc) int64 { return i64(d, "_creationTime") }

// Import loads src into the database behind db, in one transaction. The database must hold no
// organization yet (a fresh `keel serve` data dir, or one where nobody signed up).
func Import(ctx context.Context, db *sql.DB, src string) (*Report, error) {
	t, err := tables(src)
	if err != nil {
		return nil, err
	}
	_, hasProjects := t["projects"]
	_, hasUsers := t["user"]
	if !hasProjects && !hasUsers {
		return nil, fmt.Errorf("%s has no Keel tables (projects, user): is it a Convex export of a Keel install?", src)
	}
	// An install nobody signed up on exports its tables empty: that imports as nothing, so the
	// upgrade goes through and the first sign-up founds the organization as on a fresh install.
	var orgs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM organizations`).Scan(&orgs); err != nil {
		return nil, err
	}
	if orgs > 0 {
		return nil, fmt.Errorf("the database already has an organization: import into a fresh data dir")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r := &Report{Imported: map[string]int{}, Skipped: map[string]int{}}
	ex := func(table, q string, args ...any) bool {
		if _, err2 := tx.ExecContext(ctx, q, args...); err2 != nil {
			r.Skipped[table]++
			r.warn("%s: %v", table, err2)
			return false
		}
		r.Imported[table]++
		return true
	}

	// Accounts. Passwords are on Better Auth's credential account rows.
	passwords := map[string]string{}
	for _, a := range t["account"] {
		if s(a, "providerId") == "credential" && s(a, "password") != "" {
			passwords[s(a, "userId")] = s(a, "password")
		}
	}
	users := map[string]bool{}
	for _, u := range t["user"] {
		id := s(u, "_id")
		created := i64(u, "createdAt")
		if created == 0 {
			created = i64(u, "_creationTime")
		}
		updated := i64(u, "updatedAt")
		if updated == 0 {
			updated = created
		}
		if ex("users", `INSERT INTO users (id, email, name, password_hash, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
			id, strings.ToLower(strings.TrimSpace(s(u, "email"))), s(u, "name"), passwords[id], created, updated) {
			users[id] = true
		}
	}
	orgIDs := []string{}
	for _, o := range t["organization"] {
		at := i64(o, "createdAt")
		if at == 0 {
			at = created(o)
		}
		if ex("organizations", `INSERT INTO organizations (id, name, slug, created_at) VALUES (?,?,?,?)`,
			s(o, "_id"), s(o, "name"), s(o, "slug"), at) {
			orgIDs = append(orgIDs, s(o, "_id"))
		}
	}
	if len(orgIDs) > 1 {
		r.warn("%d organizations in the export; Keel expects one per install", len(orgIDs))
	}
	seenMember := map[string]bool{}
	for _, m := range t["member"] {
		key := s(m, "organizationId") + "/" + s(m, "userId")
		if seenMember[key] || !users[s(m, "userId")] {
			r.Skipped["members"]++
			continue
		}
		seenMember[key] = true
		role := s(m, "role")
		if role != "owner" && role != "admin" {
			role = "member"
		}
		at := i64(m, "createdAt")
		if at == 0 {
			at = created(m)
		}
		ex("members", `INSERT INTO members (id, organization_id, user_id, role, created_at) VALUES (?,?,?,?,?)`,
			s(m, "_id"), s(m, "organizationId"), s(m, "userId"), role, at)
	}
	for _, inv := range t["invitation"] {
		status := s(inv, "status")
		switch status {
		case "pending", "accepted", "canceled", "rejected":
		default:
			status = "canceled"
		}
		role := s(inv, "role")
		if role == "" {
			role = "member"
		}
		at := i64(inv, "createdAt")
		if at == 0 {
			at = created(inv)
		}
		ex("invitations", `INSERT INTO invitations (id, organization_id, email, role, status, inviter_id, expires_at, created_at) VALUES (?,?,?,?,?,?,?,?)`,
			s(inv, "_id"), s(inv, "organizationId"), strings.ToLower(s(inv, "email")), role, status, s(inv, "inviterId"), i64(inv, "expiresAt"), at)
	}
	// Sessions: the CLI keeps working (it sends the raw token as a bearer). Browsers sign in again:
	// their cookie was Better Auth's.
	if HashToken != nil {
		for _, ss := range t["session"] {
			if !users[s(ss, "userId")] || s(ss, "token") == "" {
				r.Skipped["sessions"]++
				continue
			}
			at := i64(ss, "createdAt")
			upd := i64(ss, "updatedAt")
			if upd == 0 {
				upd = at
			}
			ex("sessions", `INSERT INTO sessions (id, token_hash, user_id, expires_at, created_at, updated_at, user_agent, ip) VALUES (?,?,?,?,?,?,?,?)`,
				s(ss, "_id"), HashToken(s(ss, "token")), s(ss, "userId"), i64(ss, "expiresAt"), at, upd, s(ss, "userAgent"), s(ss, "ipAddress"))
		}
	} else if len(t["session"]) > 0 {
		r.Skipped["sessions"] += len(t["session"])
		r.warn("sessions not imported (no token hash configured): everyone signs in again")
	}

	// Projects. Rows from before organizations had none; the install's organization adopts them.
	defaultOrg := ""
	if len(orgIDs) == 1 {
		defaultOrg = orgIDs[0]
	}
	projectOrg := map[string]string{}
	for _, p := range t["projects"] {
		org := s(p, "organizationId")
		if org == "" {
			org = defaultOrg
		}
		if org == "" {
			r.Skipped["projects"]++
			r.warn("project %s has no organization and the export has none to adopt it", s(p, "slug"))
			continue
		}
		if ex("projects", `INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES (?,?,?,?,?)`,
			s(p, "_id"), org, s(p, "name"), s(p, "slug"), created(p)) {
			projectOrg[s(p, "_id")] = org
		}
	}
	for _, e := range t["environments"] {
		ex("environments", `INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES (?,?,?,?,?)`,
			s(e, "_id"), s(e, "projectId"), s(e, "name"), b(e, "isProduction"), created(e))
	}

	// Nodes: parents after children would violate the foreign key, so insert without parents first.
	parents := map[string]string{}
	var traced []string
	for _, n := range t["nodes"] {
		pos, cfg, des, obs := obj(n, "position"), obj(n, "config"), obj(n, "desired"), obj(n, "observed")
		var dImg, dRev, dRep, dPort any
		var dTrace int64
		if des != nil {
			dImg, dRev, dRep, dPort, dTrace = s(des, "image"), i64(des, "revision"), i64(des, "replicas"), ni64(des, "port"), b(des, "tracing")
		}
		dirty := b(n, "dirty")
		if dTrace == 1 && i64(des, "revision") > 0 {
			// Deployed with OTEL_EXPORTER_OTLP_ENDPOINT=<CONVEX_SITE_URL>/otlp (port 3211), which no
			// longer listens: stage it so "Ship · N changes" re-applies the new endpoint.
			dirty = 1
			traced = append(traced, s(n, "name"))
		}
		var oRev, oRun, oComp, oFin, oState, oIDs, oErr, oAt any
		if obs != nil {
			ids, _ := json.Marshal(arr(obs, "nodeIds"))
			if string(ids) == "null" {
				ids = []byte("[]")
			}
			oRev, oRun, oComp, oFin = i64(obs, "revision"), i64(obs, "running"), ni64(obs, "completed"), ni64(obs, "finishedAt")
			oState, oIDs, oErr, oAt = s(obs, "state"), string(ids), ns(obs, "error"), i64(obs, "at")
		}
		if pid := s(n, "parentId"); pid != "" {
			parents[s(n, "_id")] = pid
		}
		if !ex("nodes", `INSERT INTO nodes (id, environment_id, type, name, position_x, position_y,
			config_size_gb, config_width, config_height,
			desired_image, desired_revision, desired_replicas, desired_port, desired_tracing,
			observed_revision, observed_running, observed_completed, observed_finished_at, observed_state,
			observed_node_ids, observed_error, observed_at,
			deployed_revision, dirty, shipped_at, apply_error, one_shot, created_at)
			VALUES (?,?,?,?,?,?, ?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?, ?,?,?,?,?,?)`,
			s(n, "_id"), s(n, "environmentId"), s(n, "type"), s(n, "name"), float(pos, "x"), float(pos, "y"),
			nf(cfg, "sizeGb"), nf(cfg, "width"), nf(cfg, "height"),
			dImg, dRev, dRep, dPort, dTrace,
			oRev, oRun, oComp, oFin, oState, oIDs, oErr, oAt,
			ni64(n, "deployedRevision"), dirty, ni64(n, "shippedAt"), ns(n, "applyError"), b(n, "oneShot"), created(n)) {
			continue
		}
		if n["public"] != nil || n["ingress"] != nil {
			r.warn("node %s still has Quick Tunnel fields: the Convex install never ran migrations.run; expose it again", s(n, "name"))
		}
		for i, raw := range arr(n, "endpoints") {
			e, _ := raw.(map[string]any)
			st := obj(e, "status")
			state := s(st, "state")
			if state == "" {
				state = "starting"
			}
			ex("endpoints", `INSERT INTO endpoints (id, node_id, ord, protocol, port, pinned_port, domain, public_port, status_state, status_error, status_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				fmt.Sprintf("%s-%d", s(n, "_id"), i), s(n, "_id"), i, s(e, "protocol"), i64(e, "port"), b(e, "pinnedPort"),
				ns(e, "domain"), ni64(e, "publicPort"), state, ns(st, "error"), i64(st, "at"))
		}
	}
	if len(traced) > 0 {
		r.warn("ship these traced services once to point them at the new OTLP endpoint (they are staged): %s", strings.Join(traced, ", "))
	}
	for id, pid := range parents {
		if _, err := tx.ExecContext(ctx, `UPDATE nodes SET parent_id = ? WHERE id = ? AND EXISTS (SELECT 1 FROM nodes WHERE id = ?)`, pid, id, pid); err != nil {
			r.warn("node %s parent: %v", id, err)
		}
	}
	for _, v := range t["variables"] {
		ex("variables", `INSERT INTO variables (id, node_id, key, value, secret) VALUES (?,?,?,?,?)`,
			s(v, "_id"), s(v, "nodeId"), s(v, "key"), s(v, "value"), b(v, "secret"))
	}
	for _, d := range t["deployments"] {
		if !ex("deployments", `INSERT INTO deployments (id, environment_id, sha, message, status, started_at, finished_at) VALUES (?,?,?,?,?,?,?)`,
			s(d, "_id"), s(d, "environmentId"), ns(d, "sha"), s(d, "message"), s(d, "status"), i64(d, "startedAt"), ni64(d, "finishedAt")) {
			continue
		}
		for i, raw := range arr(d, "steps") {
			st, _ := raw.(map[string]any)
			ex("deployment_steps", `INSERT INTO deployment_steps (deployment_id, idx, node_id, label, status, started_at, applied_at, finished_at) VALUES (?,?,?,?,?,?,?,?)`,
				s(d, "_id"), i, ns(st, "nodeId"), s(st, "label"), s(st, "status"), ni64(st, "startedAt"), ni64(st, "appliedAt"), ni64(st, "finishedAt"))
		}
		for _, raw := range arr(d, "log") {
			l, _ := raw.(map[string]any)
			ex("deployment_log", `INSERT INTO deployment_log (deployment_id, at, node_id, text) VALUES (?,?,?,?)`,
				s(d, "_id"), i64(l, "at"), ns(l, "nodeId"), s(l, "text"))
		}
	}
	for _, c := range t["cluster"] {
		ex("cluster", `INSERT OR REPLACE INTO cluster (id, servers, at) VALUES (1, ?, ?)`, i64(c, "servers"), i64(c, "at"))
	}

	// Log sinks: one per organization; legacy rows name a project instead, and the newest stands
	// in for its organization (convex/logSinks.ts sinkOf). created_at = _creationTime matters: the
	// agent replays nothing older than when the sink was connected.
	sinkOf := map[string]doc{}
	for _, row := range t["logSinks"] {
		org := s(row, "organizationId")
		if org == "" {
			org = projectOrg[s(row, "projectId")]
		}
		if org == "" {
			r.Skipped["log_sinks"]++
			continue
		}
		prev := sinkOf[org]
		explicit := s(row, "organizationId") != ""
		if prev == nil || (explicit && s(prev, "organizationId") == "") || (explicit == (s(prev, "organizationId") != "") && created(row) > created(prev)) {
			sinkOf[org] = row
		}
	}
	for org, row := range sinkOf {
		sk := obj(row, "sink")
		ex("log_sinks", `INSERT INTO log_sinks (id, organization_id, kind, domain, dataset, traces, token, org, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			s(row, "_id"), org, s(sk, "kind"), s(sk, "domain"), s(sk, "dataset"), ns(sk, "traces"), s(sk, "token"), ns(sk, "org"), created(row))
	}
	for _, c := range t["axiomClients"] {
		ex("axiom_clients", `INSERT OR IGNORE INTO axiom_clients (redirect_uri, client_id, created_at) VALUES (?,?,?)`,
			s(c, "redirectUri"), s(c, "clientId"), created(c))
	}
	for _, k := range t["otlpKeys"] {
		ex("otlp_keys", `INSERT INTO otlp_keys (environment_id, key, created_at) VALUES (?,?,?)`,
			s(k, "environmentId"), s(k, "key"), created(k))
	}
	for _, skip := range []string{"axiomSignIns", "axiomPending", "verification", "jwks", "deviceCode"} {
		if n := len(t[skip]); n > 0 {
			r.Skipped[skip] += n
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func float(d doc, k string) float64 {
	v, _ := num(d, k)
	return v
}
