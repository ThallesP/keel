package app_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThallesP/durable"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type fakeJob struct {
	key string
	due int64
	fn  func(context.Context)
}

type fakeJobs struct {
	mu      sync.Mutex
	clock   *atomic.Int64
	pending []*fakeJob
	ctx     context.Context
}

func (j *fakeJobs) After(key string, delay time.Duration, fn func(context.Context)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if slices.ContainsFunc(j.pending, func(p *fakeJob) bool { return p.key == key }) {
		return
	}
	j.pending = append(j.pending, &fakeJob{key: key, due: j.clock.Load() + delay.Milliseconds(), fn: fn})
}

func (j *fakeJobs) Every(string, time.Duration, func(context.Context)) {}

func (j *fakeJobs) next(by int64) *fakeJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.pending) == 0 {
		return nil
	}
	p := slices.MinFunc(j.pending, func(a, b *fakeJob) int { return cmp.Compare(a.due, b.due) })
	if p.due > by {
		return nil
	}
	j.pending = slices.DeleteFunc(j.pending, func(q *fakeJob) bool { return q == p })
	return p
}

func (j *fakeJobs) run() { j.advance(0) }

func (j *fakeJobs) advance(d time.Duration) {
	target := j.clock.Load() + d.Milliseconds()
	for {
		p := j.next(target)
		if p == nil {
			j.clock.Store(target)
			return
		}
		j.clock.Store(max(j.clock.Load(), p.due))
		p.fn(j.ctx)
	}
}

func (j *fakeJobs) runOne(t *testing.T, prefix string) {
	t.Helper()
	j.mu.Lock()
	for i, p := range j.pending {
		if strings.HasPrefix(p.key, prefix) && p.due <= j.clock.Load() {
			j.pending = slices.Delete(j.pending, i, i+1)
			j.mu.Unlock()
			p.fn(j.ctx)
			return
		}
	}
	j.mu.Unlock()
	t.Fatalf("no due job %q in %v", prefix, j.keys())
}

func (j *fakeJobs) keys() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []string
	for _, p := range j.pending {
		out = append(out, fmt.Sprintf("%s@%d", p.key, p.due))
	}
	return out
}

func (j *fakeJobs) count(prefix string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, p := range j.pending {
		if strings.HasPrefix(p.key, prefix) {
			n++
		}
	}
	return n
}

func (j *fakeJobs) reset() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pending = nil
	j.ctx = context.Background()
}

type fakeService struct {
	version uint64
	spec    app.ServiceSpec
}

type fakeSwarm struct {
	mu             sync.Mutex
	cached         map[string]bool
	pullErr        map[string]error
	onPull         func(image string)
	blockedPulls   int
	onCreate       func(spec app.ServiceSpec)
	services       map[string]*fakeService
	tasks          map[string][]app.SwarmTask
	updateState    map[string]string
	updateFailures int
	creates        []app.ServiceSpec
	updates        []app.ServiceSpec
	removed        []string
	pulls          []string
	observed       []string
	ready          int
	agents         []app.AgentSpec
	versions       uint64
	undated        []string
}

func (f *fakeSwarm) call(ctx context.Context, name string) {
	if _, ok := ctx.Deadline(); !ok {
		f.undated = append(f.undated, name)
	}
}

func (f *fakeSwarm) ImageCached(ctx context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "ImageCached")
	return f.cached[image], nil
}

func (f *fakeSwarm) PullImage(ctx context.Context, image string) error {
	f.mu.Lock()
	f.call(ctx, "PullImage")
	f.pulls = append(f.pulls, image)
	onPull, blocks := f.onPull, f.blockedPulls > 0
	f.blockedPulls = max(f.blockedPulls-1, 0)
	f.mu.Unlock()
	if onPull != nil {
		onPull(image)
	}
	if blocks {
		<-ctx.Done()
		return ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.pullErr[image]; err != nil {
		return err
	}
	f.cached[image] = true
	return nil
}

func (f *fakeSwarm) ServiceVersion(ctx context.Context, id string) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "ServiceVersion")
	if s := f.services[id]; s != nil {
		return s.version, true, nil
	}
	return 0, false, nil
}

func (f *fakeSwarm) CreateService(ctx context.Context, spec app.ServiceSpec) error {
	f.mu.Lock()
	f.call(ctx, "CreateService")
	if f.services[spec.NodeID] != nil {
		f.mu.Unlock()
		return errors.New("name conflicts with an existing object")
	}
	f.versions++
	f.services[spec.NodeID] = &fakeService{version: f.versions, spec: spec}
	f.creates = append(f.creates, spec)
	onCreate := f.onCreate
	f.mu.Unlock()
	if onCreate != nil {
		onCreate(spec)
	}
	return nil
}

func (f *fakeSwarm) UpdateService(ctx context.Context, version uint64, spec app.ServiceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "UpdateService")
	if f.updateFailures > 0 {
		f.updateFailures--
		return errors.New("Error response from daemon: rpc error: update out of sequence")
	}
	s := f.services[spec.NodeID]
	if s == nil {
		return errors.New("Error response from daemon: service not found")
	}
	if s.version != version {
		return errors.New("update out of sequence")
	}
	f.versions++
	s.version, s.spec = f.versions, spec
	f.updates = append(f.updates, spec)
	return nil
}

func (f *fakeSwarm) RemoveService(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "RemoveService")
	delete(f.services, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeSwarm) view(id string) (app.SwarmService, []app.SwarmTask) {
	s := f.services[id]
	tasks, scripted := f.tasks[id]
	if s == nil {
		return app.SwarmService{}, tasks
	}
	if !scripted {
		for range s.spec.Replicas {
			tasks = append(tasks, app.SwarmTask{DesiredState: "running", State: "running", Revision: s.spec.Revision})
		}
	}
	return app.SwarmService{Name: "svc-" + id, Revision: s.spec.Revision, UpdateState: f.updateState[id]}, tasks
}

func (f *fakeSwarm) ObserveService(ctx context.Context, id string) (app.SwarmService, []app.SwarmTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "ObserveService")
	f.observed = append(f.observed, id)
	s, tasks := f.view(id)
	return s, tasks, nil
}

func (f *fakeSwarm) ObserveServices(ctx context.Context) ([]app.SwarmService, []app.SwarmTask, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "ObserveServices")
	f.observed = append(f.observed, "*")
	ids := map[string]bool{}
	for id := range f.services {
		ids[id] = true
	}
	for id := range f.tasks {
		ids[id] = true
	}
	var services []app.SwarmService
	var tasks []app.SwarmTask
	for id := range ids {
		s, ts := f.view(id)
		if s.Name != "" {
			services = append(services, s)
		}
		for _, t := range ts {
			t.NodeID = id
			tasks = append(tasks, t)
		}
	}
	return services, tasks, nil
}

func (f *fakeSwarm) Servers(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.call(ctx, "Servers")
	return f.ready, nil
}

func (f *fakeSwarm) EnsureAgent(_ context.Context, spec app.AgentSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents = append(f.agents, spec)
	return nil
}

type recorder struct {
	mu     sync.Mutex
	topics map[string][]string
}

func (r *recorder) Publish(org string, topics []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topics[org] = append(r.topics[org], topics...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.topics = map[string][]string{}
}

func (r *recorder) has(org, topic string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Contains(r.topics[org], topic)
}

type world struct {
	t         *testing.T
	app       *app.App
	store     *sqlite.Store
	jobs      *fakeJobs
	swarm     *fakeSwarm
	pub       *recorder
	clock     *atomic.Int64
	member    domain.Actor
	outsider  domain.Actor
	created   int64
	stopFlows func()
}

func newWorld(t *testing.T) *world {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for _, q := range []string{
		`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org', 'Acme', 'acme', 1), ('org2', 'Other', 'other', 2)`,
		`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p', 'org', 'Acme', 'acme', 1), ('p2', 'org2', 'Other', 'other', 1)`,
		`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env', 'p', 'production', 1, 1), ('env2', 'p2', 'production', 1, 1)`,
	} {
		if _, err := store.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	clock := &atomic.Int64{}
	clock.Store(1_000_000)
	w := &world{
		t: t, store: store, clock: clock,
		jobs: &fakeJobs{clock: clock, ctx: context.Background()},
		swarm: &fakeSwarm{
			cached: map[string]bool{}, pullErr: map[string]error{}, services: map[string]*fakeService{},
			tasks: map[string][]app.SwarmTask{}, updateState: map[string]string{},
		},
		pub:       &recorder{topics: map[string][]string{}},
		member:    domain.Actor{UserID: "u1", OrganizationID: "org", Role: domain.RoleOwner},
		outsider:  domain.Actor{UserID: "u2", OrganizationID: "org2", Role: domain.RoleOwner},
		stopFlows: func() {},
	}
	t.Cleanup(func() { w.stopFlows() })
	w.app = w.newApp(app.Config{})
	return w
}

func (w *world) newApp(cfg app.Config) *app.App {
	flows := durable.New(w.store.Workflows(), durable.PollInterval(5*time.Millisecond),
		durable.ShutdownGrace(50*time.Millisecond), durable.Logger(slog.New(slog.DiscardHandler)))
	return app.New(app.App{
		Store: w.store, Jobs: w.jobs, Workflows: flows, Swarm: w.swarm, Proxy: &igProxy{}, Events: w.pub, Config: cfg,
		Now: w.clock.Load,
		Log: slog.New(slog.DiscardHandler),
	})
}

func (w *world) start() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	flows := w.app.Workflows
	go func() {
		if err := flows.Run(ctx); err != nil {
			w.t.Error(err)
		}
		close(done)
	}()
	var once sync.Once
	w.stopFlows = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (w *world) stop() { w.stopFlows() }

func (w *world) restart(cfg app.Config) {
	w.stop()
	w.jobs.reset()
	w.app = w.newApp(cfg)
}

func (w *world) flush() {
	w.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		runs, err := w.app.Workflows.List(context.Background(), durable.RunFilter{})
		if err != nil {
			w.t.Fatal(err)
		}
		var busy []string
		for _, r := range runs {
			if r.Status == durable.StatusStalled || r.Status == durable.StatusFailed {
				w.t.Fatalf("run %s %s: %s", r.ID, r.Status, r.Error)
			}
			due := r.Status == durable.StatusWaiting && !r.WakeAt.IsZero() && !r.WakeAt.After(time.Now())
			if r.Status == durable.StatusPending || r.Status == durable.StatusRunning || due {
				busy = append(busy, r.ID+" "+string(r.Status))
			}
		}
		if len(busy) == 0 {
			return
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("workflows still busy: %v", busy)
		}
	}
}

func (w *world) waitFor(what string, done func() bool) {
	w.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			w.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (w *world) run(id string) durable.Run {
	w.t.Helper()
	info, err := w.app.Workflows.Get(context.Background(), id)
	if err != nil {
		w.t.Fatalf("run %s: %v", id, err)
	}
	return info.Run
}

type nodeOpt func(*domain.Node)

func clean(n *domain.Node)           { n.Dirty = false }
func shipped(rev int) nodeOpt        { return func(n *domain.Node) { n.Desired.Revision = rev } }
func image(img string) nodeOpt       { return func(n *domain.Node) { n.Desired.Image = img } }
func port(p int) nodeOpt             { return func(n *domain.Node) { n.Desired.Port = p } }
func inEnv(env string) nodeOpt       { return func(n *domain.Node) { n.EnvironmentID = env } }
func kind(t domain.NodeType) nodeOpt { return func(n *domain.Node) { n.Type = t } }
func applyErr(text string) nodeOpt   { return func(n *domain.Node) { n.ApplyError = text } }
func noDesired(n *domain.Node)       { n.Desired = nil }

func (w *world) addNode(name string, opts ...nodeOpt) domain.Node {
	w.t.Helper()
	w.created++
	n := domain.Node{
		ID: domain.NewID(), EnvironmentID: "env", Type: domain.NodeService, Name: name,
		Desired: &domain.Desired{Image: "nginx:alpine", Replicas: 1},
		Dirty:   true, CreatedAt: w.created,
	}
	for _, o := range opts {
		o(&n)
	}
	if n.Type == domain.NodeVolume || n.Type == domain.NodeGroup {
		n.Desired = nil
	}
	err := w.store.Write(context.Background(), func(tx app.Tx) error { return tx.InsertNode(n) })
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) node(id string) domain.Node {
	w.t.Helper()
	var n domain.Node
	err := w.store.Read(context.Background(), func(tx app.Tx) (err error) { n, err = tx.Node(id); return })
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) deleteNode(id string) {
	err := w.store.Write(context.Background(), func(tx app.Tx) error { return tx.DeleteNode(id) })
	if err != nil {
		w.t.Error(err)
	}
}

func (w *world) deployment(id string) domain.Deployment {
	w.t.Helper()
	var d domain.Deployment
	err := w.store.Read(context.Background(), func(tx app.Tx) (err error) { d, err = tx.Deployment(id); return })
	if err != nil {
		w.t.Fatal(err)
	}
	return d
}

func (w *world) ship(opts app.ShipOptions) string {
	id, err := w.app.ShipEnvironment(context.Background(), w.member, "env", opts)
	if err != nil {
		w.t.Errorf("ship: %v", err)
	}
	return id
}

func logTexts(d domain.Deployment) []string {
	var out []string
	for _, l := range d.Log {
		out = append(out, l.Text)
	}
	return out
}

func nodeLog(d domain.Deployment, nodeID string) []string {
	var out []string
	for _, l := range d.Log {
		if l.NodeID == nodeID {
			out = append(out, l.Text)
		}
	}
	return out
}

func stepStatuses(d domain.Deployment) string {
	var out []string
	for _, s := range d.Steps {
		out = append(out, s.Label+"="+string(s.Status))
	}
	return strings.Join(out, " ")
}

func codeAndMessage(err error) string {
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code + ": " + de.Message
	}
	if err == nil {
		return "<nil>"
	}
	return "untyped: " + err.Error()
}

func (w *world) failRunning(id string) {
	if _, err := w.store.DB().Exec(`UPDATE deployments SET status = 'failed', finished_at = ? WHERE id = ?`, w.clock.Load(), id); err != nil {
		w.t.Error(err)
	}
}
