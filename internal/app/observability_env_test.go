package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

const (
	obsNodeAPI     = "nodeapiaaaaaaaaaaaaa"
	obsNodeWorker  = "nodeworkerbbbbbbbbbb"
	obsNodeDB      = "nodedbcccccccccccccc"
	obsNodeVolume  = "nodevolumedddddddddd"
	obsNodeForeign = "nodeforeigneeeeeeeee"
)

type obsPublisher struct {
	mu  sync.Mutex
	got map[string][]string
}

func (p *obsPublisher) Publish(org string, topics []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got[org] = append(p.got[org], topics...)
}

func (p *obsPublisher) take(org string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	topics := p.got[org]
	delete(p.got, org)
	slices.Sort(topics)
	return slices.Compact(topics)
}

type obsJobs struct {
	every map[string]func(context.Context)
}

func (j *obsJobs) After(string, time.Duration, func(context.Context)) {}
func (j *obsJobs) Every(name string, _ time.Duration, fn func(context.Context)) {
	j.every[name] = fn
}

type obsEnv struct {
	store     *sqlite.Store
	app       *app.App
	now       int64
	pub       *obsPublisher
	jobs      *obsJobs
	member    domain.Actor
	foreigner domain.Actor
	signedOut domain.Actor
}

func newObsEnv(t *testing.T, now int64) *obsEnv {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1)`,
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org2', 'Other', 'other', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Shop', 'shop', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p2', 'org2', 'Theirs', 'theirs', 2)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env2', 'p2', 'production', 1, 2)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	desired := func() *domain.Desired { return &domain.Desired{Image: "nginx:1", Revision: 1, Replicas: 1} }
	nodes := []domain.Node{
		{ID: obsNodeAPI, EnvironmentID: "env", Type: domain.NodeService, Name: "api", Desired: desired(), CreatedAt: 1},
		{ID: obsNodeWorker, EnvironmentID: "env", Type: domain.NodeService, Name: "worker", Desired: desired(), CreatedAt: 2},
		{ID: obsNodeDB, EnvironmentID: "env", Type: domain.NodeDatabase, Name: "postgres", Desired: desired(), CreatedAt: 3},
		{ID: obsNodeVolume, EnvironmentID: "env", Type: domain.NodeVolume, Name: "data", CreatedAt: 4},
		{ID: obsNodeForeign, EnvironmentID: "env2", Type: domain.NodeService, Name: "secret-svc", Desired: desired(), CreatedAt: 5},
	}
	err = store.Write(ctx, func(tx app.Tx) error {
		for _, n := range nodes {
			if err := tx.InsertNode(n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &obsEnv{
		store:     store,
		now:       now,
		pub:       &obsPublisher{got: map[string][]string{}},
		jobs:      &obsJobs{every: map[string]func(context.Context){}},
		member:    domain.Actor{UserID: "u1", OrganizationID: "org", Role: domain.RoleMember},
		foreigner: domain.Actor{UserID: "u2", OrganizationID: "org2", Role: domain.RoleOwner},
	}
	e.app = app.New(app.App{
		Store:  store,
		Events: e.pub,
		Jobs:   e.jobs,
		Now:    func() int64 { return e.now },
		Config: app.Config{SiteURL: "https://keel.example.ts.net"},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return e
}

func (e *obsEnv) setSink(t *testing.T, org string, sink domain.LogSink) {
	t.Helper()
	err := e.store.Write(context.Background(), func(tx app.Tx) error {
		return tx.ReplaceLogSink(org, sink, e.now)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *obsEnv) node(t *testing.T, id string) domain.Node {
	t.Helper()
	var n domain.Node
	err := e.store.Read(context.Background(), func(tx app.Tx) (err error) { n, err = tx.Node(id); return })
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *obsEnv) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.store.DB().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (e *obsEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.store.DB().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func obsWantCode(t *testing.T, err error, code, msg string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code || de.Message != msg {
		t.Fatalf("error = %v, want %s %q", err, code, msg)
	}
}
