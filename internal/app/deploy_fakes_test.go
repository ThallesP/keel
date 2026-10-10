package app_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type fakeJob struct {
	key string
	due int64
	seq int
	fn  func(context.Context)
}

type fakeJobs struct {
	clock   *int64
	pending []*fakeJob
	seq     int
	ctx     context.Context
}

func (j *fakeJobs) jobCtx() context.Context {
	if j.ctx == nil {
		return context.Background()
	}
	return j.ctx
}

func (j *fakeJobs) After(key string, delay time.Duration, fn func(context.Context)) {
	for _, p := range j.pending {
		if p.key == key {
			return
		}
	}
	j.seq++
	j.pending = append(j.pending, &fakeJob{key: key, due: *j.clock + delay.Milliseconds(), seq: j.seq, fn: fn})
}

func (j *fakeJobs) Every(string, time.Duration, func(context.Context)) {}

func (j *fakeJobs) next(by int64) *fakeJob {
	idx := -1
	for i, p := range j.pending {
		if p.due > by {
			continue
		}
		if idx < 0 || p.due < j.pending[idx].due || p.due == j.pending[idx].due && p.seq < j.pending[idx].seq {
			idx = i
		}
	}
	if idx < 0 {
		return nil
	}
	p := j.pending[idx]
	j.pending = append(j.pending[:idx], j.pending[idx+1:]...)
	return p
}

func (j *fakeJobs) run() { j.advance(0) }

func (j *fakeJobs) advance(d time.Duration) {
	target := *j.clock + d.Milliseconds()
	for {
		p := j.next(target)
		if p == nil {
			*j.clock = target
			return
		}
		if p.due > *j.clock {
			*j.clock = p.due
		}
		p.fn(j.jobCtx())
	}
}

func (j *fakeJobs) runOne(t *testing.T, prefix string) {
	t.Helper()
	for i, p := range j.pending {
		if strings.HasPrefix(p.key, prefix) && p.due <= *j.clock {
			j.pending = append(j.pending[:i], j.pending[i+1:]...)
			p.fn(j.jobCtx())
			return
		}
	}
	t.Fatalf("no due job %q in %v", prefix, j.keys())
}

func (j *fakeJobs) keys() []string {
	var out []string
	for _, p := range j.pending {
		out = append(out, fmt.Sprintf("%s@%d", p.key, p.due))
	}
	return out
}

func (j *fakeJobs) count(prefix string) int {
	n := 0
	for _, p := range j.pending {
		if strings.HasPrefix(p.key, prefix) {
			n++
		}
	}
	return n
}

type fakeService struct {
	version uint64
	spec    app.ServiceSpec
}

type fakeSwarm struct {
	cached                   map[string]bool
	pullErr                  map[string]error
	onPull                   func(image string)
	pullBlocksUntilCancelled bool
	onCreate                 func(spec app.ServiceSpec)
	services                 map[string]*fakeService
	tasks                    map[string][]app.SwarmTask
	updateState              map[string]string
	updateFailures           int
	creates                  []app.ServiceSpec
	updates                  []app.ServiceSpec
	removed                  []string
	pulls                    []string
	observed                 []string
	ready, total             int
	agents                   []app.AgentSpec
	tunnelSweeps             int
	versions                 uint64
	undated                  []string
}

func (f *fakeSwarm) call(ctx context.Context, name string) {
	if _, ok := ctx.Deadline(); !ok {
		f.undated = append(f.undated, name)
	}
}

func newFakeSwarm() *fakeSwarm {
	return &fakeSwarm{
		cached: map[string]bool{}, pullErr: map[string]error{}, services: map[string]*fakeService{},
		tasks: map[string][]app.SwarmTask{}, updateState: map[string]string{},
	}
}

func (f *fakeSwarm) ImageCached(ctx context.Context, image string) (bool, error) {
	f.call(ctx, "ImageCached")
	return f.cached[image], nil
}

func (f *fakeSwarm) PullImage(ctx context.Context, image string) error {
	f.call(ctx, "PullImage")
	f.pulls = append(f.pulls, image)
	if f.onPull != nil {
		f.onPull(image)
	}
	if f.pullBlocksUntilCancelled {
		f.pullBlocksUntilCancelled = false
		<-ctx.Done()
		return ctx.Err()
	}
	if err := f.pullErr[image]; err != nil {
		return err
	}
	f.cached[image] = true
	return nil
}

func (f *fakeSwarm) ServiceVersion(ctx context.Context, id string) (uint64, bool, error) {
	f.call(ctx, "ServiceVersion")
	if s := f.services[id]; s != nil {
		return s.version, true, nil
	}
	return 0, false, nil
}

func (f *fakeSwarm) CreateService(ctx context.Context, spec app.ServiceSpec) error {
	f.call(ctx, "CreateService")
	if f.services[spec.NodeID] != nil {
		return errors.New("name conflicts with an existing object")
	}
	f.versions++
	f.services[spec.NodeID] = &fakeService{version: f.versions, spec: spec}
	f.creates = append(f.creates, spec)
	if f.onCreate != nil {
		f.onCreate(spec)
	}
	return nil
}

func (f *fakeSwarm) UpdateService(ctx context.Context, version uint64, spec app.ServiceSpec) error {
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
	f.call(ctx, "RemoveService")
	delete(f.services, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeSwarm) view(id string) (*app.SwarmService, []app.SwarmTask) {
	s := f.services[id]
	if tasks, ok := f.tasks[id]; ok {
		if s == nil {
			return nil, tasks
		}
		return &app.SwarmService{Name: "svc-" + id, Labels: map[string]string{"keel.service": id, "keel.revision": strconv.Itoa(s.spec.Revision)}, UpdateState: f.updateState[id]}, tasks
	}
	if s == nil {
		return nil, nil
	}
	labels := map[string]string{"keel.service": id, "keel.revision": strconv.Itoa(s.spec.Revision)}
	var tasks []app.SwarmTask
	for i := 0; i < s.spec.Replicas; i++ {
		tasks = append(tasks, app.SwarmTask{NodeID: "swarm-1", DesiredState: "running", State: "running", Labels: labels})
	}
	return &app.SwarmService{Name: "svc-" + id, Labels: labels, UpdateState: f.updateState[id]}, tasks
}

func (f *fakeSwarm) ObserveService(ctx context.Context, id string) (*app.SwarmService, []app.SwarmTask, error) {
	f.call(ctx, "ObserveService")
	f.observed = append(f.observed, id)
	s, tasks := f.view(id)
	return s, tasks, nil
}

func (f *fakeSwarm) ObserveServices(ctx context.Context) ([]app.SwarmService, []app.SwarmTask, error) {
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
		if s != nil {
			services = append(services, *s)
		}
		for _, t := range ts {
			if t.Labels == nil {
				t.Labels = map[string]string{}
			}
			t.Labels["keel.service"] = id
			tasks = append(tasks, t)
		}
	}
	return services, tasks, nil
}

func (f *fakeSwarm) Servers(ctx context.Context) (int, int, error) {
	f.call(ctx, "Servers")
	return f.ready, f.total, nil
}

func (f *fakeSwarm) EnsureAgent(_ context.Context, spec app.AgentSpec) error {
	f.agents = append(f.agents, spec)
	return nil
}

func (f *fakeSwarm) RemoveLegacyTunnels(ctx context.Context) (int, error) {
	f.call(ctx, "RemoveLegacyTunnels")
	f.tunnelSweeps++
	return 0, nil
}

type recorder struct{ topics map[string][]string }

func (r *recorder) Publish(org string, topics []string) {
	r.topics[org] = append(r.topics[org], topics...)
}

func (r *recorder) reset() { r.topics = map[string][]string{} }

func (r *recorder) has(org, topic string) bool {
	for _, t := range r.topics[org] {
		if t == topic {
			return true
		}
	}
	return false
}

type world struct {
	t        *testing.T
	app      *app.App
	store    *sqlite.Store
	jobs     *fakeJobs
	swarm    *fakeSwarm
	pub      *recorder
	clock    *int64
	member   domain.Actor
	outsider domain.Actor
	follows  []int
	syncs    int
	envVars  map[string]map[string]string
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
	clock := int64(1_000_000)
	w := &world{
		t: t, store: store, clock: &clock,
		jobs:     &fakeJobs{clock: &clock},
		swarm:    newFakeSwarm(),
		pub:      &recorder{topics: map[string][]string{}},
		member:   domain.Actor{UserID: "u1", OrganizationID: "org", Role: domain.RoleOwner},
		outsider: domain.Actor{UserID: "u2", OrganizationID: "org2", Role: domain.RoleOwner},
		envVars:  map[string]map[string]string{},
	}
	w.app = w.newApp(app.Config{})
	restore := app.UseDeploySeams(app.DeploySeams{
		ComputeEnv: func(_ app.Tx, n domain.Node) (map[string]string, error) {
			env := map[string]string{}
			for k, v := range w.envVars[n.ID] {
				env[k] = v
			}
			return env, nil
		},
		FollowPort: func(_ app.Tx, _ *app.Changes, _ app.NodeScope, port int, _ int64) (bool, error) {
			w.follows = append(w.follows, port)
			return port == 9999, nil
		},
		ProxySync: func() { w.syncs++ },
	})
	t.Cleanup(restore)
	return w
}

func (w *world) newApp(cfg app.Config) *app.App {
	return app.New(app.App{
		Store: w.store, Jobs: w.jobs, Swarm: w.swarm, Events: w.pub, Config: cfg,
		Now: func() int64 { return *w.clock },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func (w *world) restart(cfg app.Config) {
	w.jobs.pending = nil
	w.jobs.ctx = nil
	w.app = w.newApp(cfg)
}

type nodeOpt func(*domain.Node)

func clean(n *domain.Node)           { n.Dirty = false }
func shipped(rev int) nodeOpt        { return func(n *domain.Node) { n.Desired.Revision = rev } }
func image(img string) nodeOpt       { return func(n *domain.Node) { n.Desired.Image = img } }
func replicas(r int) nodeOpt         { return func(n *domain.Node) { n.Desired.Replicas = r } }
func port(p int) nodeOpt             { return func(n *domain.Node) { n.Desired.Port = &p } }
func inEnv(env string) nodeOpt       { return func(n *domain.Node) { n.EnvironmentID = env } }
func kind(t domain.NodeType) nodeOpt { return func(n *domain.Node) { n.Type = t } }
func applyErr(text string) nodeOpt   { return func(n *domain.Node) { n.ApplyError = text } }
func noDesired(n *domain.Node)       { n.Desired = nil }

var nodeSeq int64

func (w *world) addNode(name string, opts ...nodeOpt) domain.Node {
	w.t.Helper()
	nodeSeq++
	n := domain.Node{
		ID: domain.NewID(), EnvironmentID: "env", Type: domain.NodeService, Name: name,
		Desired: &domain.Desired{Image: "nginx:alpine", Revision: 0, Replicas: 1},
		Dirty:   true, CreatedAt: nodeSeq,
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
	w.t.Helper()
	err := w.store.Write(context.Background(), func(tx app.Tx) error { return tx.DeleteNode(id) })
	if err != nil {
		w.t.Fatal(err)
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
	w.t.Helper()
	id, err := w.app.ShipEnvironment(context.Background(), w.member, "env", opts)
	if err != nil {
		w.t.Fatalf("ship: %v", err)
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

func stepStatuses(d domain.Deployment) string {
	var out []string
	for _, s := range d.Steps {
		out = append(out, s.Label+"="+string(s.Status))
	}
	return strings.Join(out, " ")
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
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
	w.t.Helper()
	if _, err := w.store.DB().Exec(`UPDATE deployments SET status = 'failed', finished_at = ? WHERE id = ?`, *w.clock, id); err != nil {
		w.t.Fatal(err)
	}
}
