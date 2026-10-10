package app_test

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type canvasPublisher struct {
	mu     sync.Mutex
	topics map[string][]string
}

func (p *canvasPublisher) Publish(org string, topics []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.topics == nil {
		p.topics = map[string][]string{}
	}
	p.topics[org] = append(p.topics[org], topics...)
}

func (p *canvasPublisher) take(org string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	set := map[string]bool{}
	for _, t := range p.topics[org] {
		set[t] = true
	}
	delete(p.topics, org)
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

type canvasShipCall struct {
	Environment string
	Opts        app.ShipOptions
}

type canvasKit struct {
	t     *testing.T
	ctx   context.Context
	store *sqlite.Store
	app   *app.App
	pub   *canvasPublisher
	now   int64

	ships    []canvasShipCall
	shipErr  error
	proxy    int
	removed  []string
	observed []string
}

const (
	canvasOrg   = "org-a"
	canvasOther = "org-b"
)

func canvasSetup(t *testing.T) *canvasKit {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	k := &canvasKit{t: t, ctx: ctx, store: store, pub: &canvasPublisher{}, now: 1_000}
	k.exec(`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org-a', 'A', 'a', 1), ('org-b', 'B', 'b', 1)`)
	k.app = app.New(app.App{Store: store, Events: k.pub, Now: func() int64 { k.now++; return k.now }, Config: app.Config{PublicIP: "203.0.113.7"}})
	app.StubCanvasSeams(t, app.CanvasSeams{
		Ship: func(a *app.App, tx app.Tx, ch *app.Changes, scope app.EnvScope, opts app.ShipOptions) (string, error) {
			if k.shipErr != nil {
				return "", k.shipErr
			}
			k.ships = append(k.ships, canvasShipCall{Environment: scope.Environment.ID, Opts: opts})
			return "dep-" + scope.Environment.ID, nil
		},
		Schedulers: &app.CanvasSchedulers{
			ProxySync:     func() { k.proxy++ },
			RemoveService: func(id string) { k.removed = append(k.removed, id) },
			Observe:       func(id string) { k.observed = append(k.observed, id) },
		},
	})
	return k
}

func (k *canvasKit) exec(q string, args ...any) {
	k.t.Helper()
	if _, err := k.store.DB().Exec(q, args...); err != nil {
		k.t.Fatal(err)
	}
}

func canvasMember(org string) domain.Actor {
	return domain.Actor{UserID: "user-" + org, Email: org + "@example.com", OrganizationID: org, Role: domain.RoleMember}
}

func (k *canvasKit) project(org, name string) string {
	k.t.Helper()
	p, err := k.app.CreateProject(k.ctx, canvasMember(org), name)
	if err != nil {
		k.t.Fatal(err)
	}
	return p.Environments[0].ID
}

func (k *canvasKit) create(env string, in app.CreateNodeInput) string {
	k.t.Helper()
	out, err := k.app.CreateNode(k.ctx, canvasMember(canvasOrg), env, in)
	if err != nil {
		k.t.Fatalf("create %+v: %v", in, err)
	}
	return out.ID
}

func (k *canvasKit) node(id string) domain.Node {
	k.t.Helper()
	var n domain.Node
	err := k.store.Read(k.ctx, func(tx app.Tx) (err error) { n, err = tx.Node(id); return })
	if err != nil {
		k.t.Fatalf("node %s: %v", id, err)
	}
	return n
}

func (k *canvasKit) exists(id string) bool {
	k.t.Helper()
	err := k.store.Read(k.ctx, func(tx app.Tx) error { _, err := tx.Node(id); return err })
	return err == nil
}

func (k *canvasKit) vars(id string) []domain.Variable {
	k.t.Helper()
	var vs []domain.Variable
	err := k.store.Read(k.ctx, func(tx app.Tx) (err error) { vs, err = tx.CanvasVariables(id); return })
	if err != nil {
		k.t.Fatal(err)
	}
	return vs
}

func (k *canvasKit) setVar(id, key, value string) {
	k.t.Helper()
	if err := k.app.SetVariable(k.ctx, canvasMember(canvasOrg), id, app.SetVariableInput{Key: key, Value: value}); err != nil {
		k.t.Fatalf("set %s=%s: %v", key, value, err)
	}
}

func (k *canvasKit) clean(env string) {
	k.exec(`UPDATE nodes SET dirty = 0 WHERE environment_id = ?`, env)
}

func canvasPtr[T any](v T) *T { return &v }

func canvasWantErr(t *testing.T, err error, code, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s %q", code, msg)
	}
	if domain.CodeOf(err) != code || err.Error() != msg {
		t.Fatalf("error = %s %q, want %s %q", domain.CodeOf(err), err.Error(), code, msg)
	}
}

func canvasKeys(vs []domain.Variable) string {
	keys := make([]string, 0, len(vs))
	for _, v := range vs {
		keys = append(keys, v.Key)
	}
	return strings.Join(keys, ",")
}
