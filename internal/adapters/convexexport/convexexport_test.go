package convexexport

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
)

var export = map[string]string{
	"_components/betterAuth/user/documents.jsonl":         `{"_id":"u1","_creationTime":1.5,"name":"Ada","email":"Ada@Example.com","emailVerified":false,"createdAt":10,"updatedAt":11}`,
	"_components/betterAuth/account/documents.jsonl":      `{"_id":"a1","_creationTime":1,"accountId":"u1","providerId":"credential","userId":"u1","password":"00:11","createdAt":10,"updatedAt":10}`,
	"_components/betterAuth/organization/documents.jsonl": `{"_id":"o1","_creationTime":2,"name":"Default","slug":"default","createdAt":12}`,
	"_components/betterAuth/member/documents.jsonl":       `{"_id":"m1","_creationTime":3,"organizationId":"o1","userId":"u1","role":"owner","createdAt":13}`,
	"_components/betterAuth/session/documents.jsonl":      `{"_id":"s1","_creationTime":4,"token":"tok","userId":"u1","expiresAt":99999999999999,"createdAt":14,"updatedAt":15}`,
	"projects/documents.jsonl": `{"_id":"p1","_creationTime":20,"name":"Acme","slug":"acme","organizationId":"o1"}
{"_id":"p2","_creationTime":21,"name":"Legacy","slug":"legacy","ownerId":"u1"}`,
	"environments/documents.jsonl": `{"_id":"e1","_creationTime":22,"projectId":"p1","name":"production","isProduction":true}`,
	"nodes/documents.jsonl": `{"_id":"child","_creationTime":30,"environmentId":"e1","type":"service","name":"api","parentId":"grp","position":{"x":1.5,"y":2},"config":{},"desired":{"image":"nginx:1","revision":2,"replicas":1,"port":80},"observed":{"revision":2,"running":1,"state":"ok","nodeIds":["sw1"],"at":40},"endpoints":[{"protocol":"http","port":80,"domain":"api.example.com","status":{"state":"live","at":41}}],"dirty":true,"shippedAt":35}
{"_id":"traced","_creationTime":30.5,"environmentId":"e1","type":"service","name":"worker","position":{"x":0,"y":0},"config":{},"desired":{"image":"app:1","revision":3,"replicas":1,"tracing":true},"dirty":false}
{"_id":"grp","_creationTime":31,"environmentId":"e1","type":"group","name":"g","position":{"x":0,"y":0},"config":{"width":400,"height":300}}`,
	"variables/documents.jsonl":    `{"_id":"v1","_creationTime":32,"nodeId":"child","key":"PORT","value":"80","secret":false}`,
	"deployments/documents.jsonl":  `{"_id":"d1","_creationTime":33,"environmentId":"e1","message":"ship api","status":"success","startedAt":34,"finishedAt":36,"steps":[{"nodeId":"child","label":"api","status":"done","startedAt":34,"appliedAt":35,"finishedAt":36},{"label":"health checks","status":"done"}],"log":[{"at":35,"nodeId":"child","text":"pulled nginx:1 in 1.0s"}]}`,
	"logSinks/documents.jsonl":     `{"_id":"ls1","_creationTime":50,"projectId":"p1","sink":{"kind":"axiom","domain":"api.axiom.co","dataset":"keel-logs","token":"xaat-1"}}`,
	"otlpKeys/documents.jsonl":     `{"_id":"k1","_creationTime":51,"environmentId":"e1","key":"keel_otlp_abc"}`,
	"axiomPending/documents.jsonl": `{"_id":"x","_creationTime":52,"organizationId":"o1","token":"t","orgs":[]}`,
}

func writeZip(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "snapshot.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, body := range export {
		w, _ := z.Create(name)
		w.Write([]byte(body + "\n"))
	}
	z.Close()
	f.Close()
	return p
}

func TestImport(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	HashToken = func(s string) string { return "h:" + s }
	defer func() { HashToken = nil }()

	rep, err := Import(ctx, store.DB(), writeZip(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "worker") {
		t.Fatalf("warnings: %v", rep.Warnings)
	}
	var tracedDirty int
	store.DB().QueryRow(`SELECT dirty FROM nodes WHERE id='traced'`).Scan(&tracedDirty)
	if tracedDirty != 1 {
		t.Errorf("a traced service is staged after import, dirty=%d", tracedDirty)
	}
	want := map[string]int{"users": 1, "organizations": 1, "members": 1, "sessions": 1, "projects": 2,
		"environments": 1, "nodes": 3, "endpoints": 1, "variables": 1, "deployments": 1,
		"deployment_steps": 2, "deployment_log": 1, "log_sinks": 1, "otlp_keys": 1}
	for k, v := range want {
		if rep.Imported[k] != v {
			t.Errorf("imported %s = %d, want %d", k, rep.Imported[k], v)
		}
	}
	if rep.Skipped["axiomPending"] != 1 {
		t.Errorf("skipped: %v", rep.Skipped)
	}

	var email, org, parent, hash string
	db := store.DB()
	db.QueryRow(`SELECT email FROM users WHERE id='u1'`).Scan(&email)
	db.QueryRow(`SELECT organization_id FROM projects WHERE id='p2'`).Scan(&org)
	db.QueryRow(`SELECT parent_id FROM nodes WHERE id='child'`).Scan(&parent)
	db.QueryRow(`SELECT token_hash FROM sessions WHERE id='s1'`).Scan(&hash)
	if email != "ada@example.com" || org != "o1" || parent != "grp" || hash != "h:tok" {
		t.Errorf("email=%q legacy org=%q parent=%q hash=%q", email, org, parent, hash)
	}

	err = store.Read(ctx, func(tx app.Tx) error {
		n, err := tx.Node("child")
		if err != nil {
			return err
		}
		if n.Desired == nil || n.Desired.Revision != 2 || n.Observed == nil || n.Observed.NodeIDs[0] != "sw1" ||
			len(n.Endpoints) != 1 || n.Endpoints[0].Domain != "api.example.com" || !n.Dirty || n.Position.X != 1.5 {
			t.Errorf("node: %+v", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A second import into the same database refuses.
	if _, err := Import(ctx, store.DB(), writeZip(t)); err == nil || !strings.Contains(err.Error(), "already has an organization") {
		t.Errorf("second import: %v", err)
	}
}

// An install nobody signed up on exports empty tables: that is an empty import, not an error.
func TestImportEmptyInstall(t *testing.T) {
	dir := t.TempDir()
	for _, table := range []string{"projects", "nodes", "_components/betterAuth/user"} {
		if err := os.MkdirAll(filepath.Join(dir, table), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, table, "documents.jsonl"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := Import(context.Background(), store.DB(), dir); err != nil {
		t.Fatalf("empty install: %v", err)
	}
	if _, err := Import(context.Background(), store.DB(), t.TempDir()); err == nil {
		t.Fatal("a directory with no Keel tables imports")
	}
}
