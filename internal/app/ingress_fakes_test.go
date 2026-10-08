package app_test

// Fakes and fixtures of the ingress use-case tests. Names carry an `ig` prefix: every area's
// tests share package app_test.

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// igJobs is an in-memory Jobs: After coalesces by key until Run drains it.
type igJobs struct {
	mu      sync.Mutex
	pending map[string]func(context.Context)
	order   []string
	every   map[string]time.Duration
	everyFn map[string]func(context.Context)
}

func (j *igJobs) After(key string, _ time.Duration, fn func(context.Context)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := j.pending[key]; ok {
		return
	}
	j.pending[key] = fn
	j.order = append(j.order, key)
}

func (j *igJobs) Every(name string, interval time.Duration, fn func(context.Context)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.every[name] = interval
	j.everyFn[name] = fn
}

func (j *igJobs) Pending(key string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.pending[key]
	return ok
}

// Run runs pending jobs (and the ones they schedule) until none is left.
func (j *igJobs) Run(t *testing.T) {
	t.Helper()
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("jobs keep rescheduling themselves")
		}
		j.mu.Lock()
		if len(j.order) == 0 {
			j.mu.Unlock()
			return
		}
		key := j.order[0]
		j.order = j.order[1:]
		fn := j.pending[key]
		delete(j.pending, key)
		j.mu.Unlock()
		fn(context.Background())
	}
}

// igProxy is keel-proxy's admin API.
type igProxy struct {
	mu        sync.Mutex
	addrs     []string
	addrsErr  error
	loads     []string
	loadErrs  []error // one per LoadApps call, nil once exhausted
	onLoad    func(call int)
	certs     map[string]app.ProxyCert
	certsErr  error
	certCalls [][]string
	onCerts   func() // runs after Certs took its answer, before the caller sees it
	reportURL string
}

func (p *igProxy) HostAddrs(context.Context) ([]string, error) { return p.addrs, p.addrsErr }

func (p *igProxy) LoadApps(_ context.Context, apps []byte) error {
	p.mu.Lock()
	p.loads = append(p.loads, string(apps))
	call := len(p.loads)
	var err error
	if len(p.loadErrs) > 0 {
		err, p.loadErrs = p.loadErrs[0], p.loadErrs[1:]
	}
	hook := p.onLoad
	p.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	return err
}

func (p *igProxy) Certs(_ context.Context, names []string) (map[string]app.ProxyCert, error) {
	p.mu.Lock()
	p.certCalls = append(p.certCalls, names)
	certs, err, hook := p.certs, p.certsErr, p.onCerts
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return certs, err
}

// loadCount is how many configs were pushed so far.
func (p *igProxy) loadCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.loads)
}

func (p *igProxy) ReportURL() string { return p.reportURL }

type igPublish struct {
	org    string
	topics []string
}

type igPublisher struct {
	mu  sync.Mutex
	got []igPublish
}

func (p *igPublisher) Publish(org string, topics []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, igPublish{org, topics})
}

// take returns and clears what was published.
func (p *igPublisher) take() []igPublish {
	p.mu.Lock()
	defer p.mu.Unlock()
	got := p.got
	p.got = nil
	return got
}

type igEnv struct {
	t      *testing.T
	ctx    context.Context
	store  *sqlite.Store
	app    *app.App
	jobs   *igJobs
	proxy  *igProxy
	pub    *igPublisher
	now    int64
	seq    int64
	member domain.Actor // organization "org"
	other  domain.Actor // organization "org-b"
}

const (
	igEnvA = "env"
	igEnvB = "env-b"
	igIP   = "203.0.113.7"
)

func newIngressEnv(t *testing.T) *igEnv {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1), ('org-b', 'Other', 'other', 1)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Shop', 'shop', 1), ('p-b', 'org-b', 'Blog', 'blog', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1), ('env-b', 'p-b', 'production', 1, 1)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e := &igEnv{
		t: t, ctx: context.Background(), store: store, now: 1_000,
		jobs:   &igJobs{pending: map[string]func(context.Context){}, every: map[string]time.Duration{}, everyFn: map[string]func(context.Context){}},
		proxy:  &igProxy{addrs: []string{igIP}},
		pub:    &igPublisher{},
		member: domain.Actor{UserID: "u1", OrganizationID: "org", Role: domain.RoleOwner},
		other:  domain.Actor{UserID: "u2", OrganizationID: "org-b", Role: domain.RoleOwner},
	}
	e.app = app.New(app.App{
		Store: store, Events: e.pub, Jobs: e.jobs, Proxy: e.proxy,
		Config: app.Config{PublicIP: igIP, WorkerToken: "tok", SiteURL: "https://keel.example.ts.net"},
		Now:    func() int64 { return e.now },
	})
	return e
}

type igNodeOpt func(*domain.Node)

func igNoPort(n *domain.Node)    { n.Desired.Port = nil }
func igDirty(n *domain.Node)     { n.Dirty = true }
func igNoDesired(n *domain.Node) { n.Desired = nil }
func igUndeployed(n *domain.Node) {
	n.DeployedRevision = nil
}

// node inserts a shipped, converged node.
func (e *igEnv) node(id, env string, typ domain.NodeType, name, image string, port int, opts ...igNodeOpt) domain.Node {
	e.t.Helper()
	e.seq++
	rev := 1
	n := domain.Node{
		ID: id, EnvironmentID: env, Type: typ, Name: name, CreatedAt: e.seq,
		Desired:          &domain.Desired{Image: image, Revision: 1, Replicas: 1, Port: &port},
		DeployedRevision: &rev,
	}
	for _, o := range opts {
		o(&n)
	}
	if err := e.store.Write(e.ctx, func(tx app.Tx) error { return tx.InsertNode(n) }); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *igEnv) variable(nodeID, key, value string) {
	e.t.Helper()
	if _, err := e.store.DB().Exec(`INSERT INTO variables (id, node_id, key, value, secret) VALUES (?, ?, ?, ?, 1)`,
		domain.NewID(), nodeID, key, value); err != nil {
		e.t.Fatal(err)
	}
}

func (e *igEnv) endpoints(nodeID string) []domain.Endpoint {
	e.t.Helper()
	var n domain.Node
	if err := e.store.Read(e.ctx, func(tx app.Tx) (err error) { n, err = tx.Node(nodeID); return }); err != nil {
		e.t.Fatal(err)
	}
	return n.Endpoints
}

func (e *igEnv) setEndpoints(nodeID string, eps ...domain.Endpoint) {
	e.t.Helper()
	if err := e.store.Write(e.ctx, func(tx app.Tx) error { return tx.ReplaceEndpoints(nodeID, eps) }); err != nil {
		e.t.Fatal(err)
	}
}

func (e *igEnv) expose(actor domain.Actor, nodeID string, in app.ExposeInput) (domain.Endpoint, error) {
	return e.app.Expose(e.ctx, actor, nodeID, in)
}

func igF(v float64) *float64 { return &v }
func igS(v string) *string   { return &v }
func igI(v int) *int         { return &v }

// wantErr checks a domain error's code and message.
func igWantErr(t *testing.T, err error, code, message string) {
	t.Helper()
	de, ok := err.(*domain.Error)
	if !ok || de.Code != code || de.Message != message {
		t.Fatalf("got error %#v, want %s %q", err, code, message)
	}
}

func igTopics(got []igPublish) []string {
	var out []string
	for _, p := range got {
		for _, topic := range p.topics {
			out = append(out, p.org+" "+topic)
		}
	}
	slices.Sort(out)
	return out
}
