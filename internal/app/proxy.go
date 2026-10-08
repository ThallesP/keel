package app

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// keel-proxy sync: make the edge serve exactly the endpoints in the database
// (docs/go/spec/proxy-ingress.md §5). Whole-config and idempotent: the builder (proxy_config.go)
// turns every endpoint into Caddy's `apps` object, the Proxy port loads it, and each endpoint's
// status records the outcome.

const (
	proxySyncKey    = "proxy:sync"
	proxyResyncName = "proxy:resync"
	// ProxyResyncInterval: retry what failed for a passing reason (proxy restarting, a port
	// freed), pick up a changed host address, correct a status that lost a race.
	ProxyResyncInterval = 2 * time.Minute
	proxySyncPasses     = 3
	// MsgNoHostAddress: /keel/host-addrs found nothing to bind (Q2: also when it answers null).
	MsgNoHostAddress = "keel-proxy found no public network address on the control plane"
)

// Certificate events keel-proxy reports (POST /proxy/events).
const (
	CertObtained = "cert_obtained"
	CertFailed   = "cert_failed"
)

// ingressState is the per-App state of the sync loop. App's fields belong to the foundation, so
// it lives beside it, keyed by the App.
type ingressState struct {
	// mu guards running and again: the coalescing trigger of proxy-ingress.md §5.1. One sync runs
	// at a time; however many are asked for while it runs (Jobs only coalesces syncs that have not
	// started), they collapse into one more pass after it, and none of them waits for it.
	mu      sync.Mutex
	running bool
	again   bool
	// failed: the last sync could not load any config (proxy down, an error no endpoint owns).
	// The resync keeps retrying then even with nothing exposed (Q6).
	failed atomic.Bool
	// resyncArmed: the 2-minute resync is registered (once per App).
	resyncArmed atomic.Bool
}

var ingressStates sync.Map // *App → *ingressState

func (a *App) ingress() *ingressState {
	v, _ := ingressStates.LoadOrStore(a, &ingressState{})
	return v.(*ingressState)
}

// begin claims the sync loop. false: a sync is running, and it will go again for this caller.
func (st *ingressState) begin() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running {
		st.again = true
		return false
	}
	st.running = true
	return true
}

// next says whether the running sync must go again (someone asked meanwhile), and otherwise
// frees the loop in the same critical section, so no request can fall in between.
func (st *ingressState) next() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.again {
		st.again = false
		return true
	}
	st.running = false
	return false
}

// release frees the loop after a sync that did not finish (cancelled, or panicked): the next
// request runs instead of finding the loop taken forever.
func (st *ingressState) release() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.running, st.again = false, false
}

// againIfRunning makes a sync that is running now go once more when it is done.
func (st *ingressState) againIfRunning() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running {
		st.again = true
	}
}

// ScheduleProxySync rebuilds and loads keel-proxy's config soon (coalesced by Jobs). Safe to call
// often; call it after the triggering write commits (ch.AfterCommit).
func (a *App) ScheduleProxySync() {
	if a.Jobs == nil {
		return
	}
	a.Jobs.After(proxySyncKey, 0, a.SyncProxy)
}

type proxyReporter struct{ URL, Token string }

type proxyACME struct{ CA, Email string }

// routeStatus is one endpoint's outcome of a sync pass.
type routeStatus struct {
	NodeID string
	Key    string
	Port   int
	Status domain.EndpointStatus
}

// SyncProxy loads the config for every endpoint and records each endpoint's status. When the
// endpoints changed while it loaded, it goes again (at most 3 passes), so the latest set wins.
// Called while another sync runs, it returns at once and that sync runs once more when it is
// done (however many calls came meanwhile), so syncs never overlap and never queue up.
func (a *App) SyncProxy(ctx context.Context) {
	if a.Proxy == nil {
		a.Log.Warn("keel-proxy sync skipped: no proxy configured")
		return
	}
	st := a.ingress()
	if !st.begin() {
		return
	}
	finished := false
	defer func() {
		if !finished {
			st.release()
		}
	}()
	for ctx.Err() == nil {
		a.syncProxy(ctx, st)
		if !st.next() {
			finished = true
			return
		}
	}
}

// syncProxy is one sync: up to 3 passes, until the endpoints did not move during a load.
func (a *App) syncProxy(ctx context.Context, st *ingressState) {
	reporter := proxyReporter{URL: a.Proxy.ReportURL(), Token: a.Config.WorkerToken}
	if reporter.URL == "" {
		reporter.URL = a.Config.SiteURL + "/proxy/events"
	}
	acme := proxyACME{CA: a.Config.ACMECA, Email: a.Config.ACMEEmail}
	for range proxySyncPasses {
		routes, err := a.proxyRoutes(ctx)
		if err != nil {
			a.Log.Error("keel-proxy sync: read endpoints", "err", err)
			return
		}
		statuses, loaded := a.applyProxy(ctx, routes, reporter, acme)
		st.failed.Store(!loaded)
		if err := a.setEndpointStatuses(ctx, statuses); err != nil {
			a.Log.Error("keel-proxy sync: write statuses", "err", err)
			return
		}
		after, err := a.proxyRoutes(ctx)
		if err != nil {
			a.Log.Error("keel-proxy sync: read endpoints", "err", err)
			return
		}
		if slices.Equal(after, routes) {
			return
		}
	}
}

func (a *App) proxyRoutes(ctx context.Context) ([]ProxyRoute, error) {
	var routes []ProxyRoute
	err := a.read(ctx, func(tx Tx) (err error) {
		routes, err = tx.IngressRoutes()
		return err
	})
	return routes, err
}

// applyProxy loads the config for routes and says how each endpoint stands (§5.1.1). loaded is
// false when no config could be loaded at all (the proxy keeps serving its previous one).
func (a *App) applyProxy(ctx context.Context, routes []ProxyRoute, rep proxyReporter, acme proxyACME) ([]routeStatus, bool) {
	at := a.Now()
	statusOf := func(r ProxyRoute, s domain.EndpointStatus) routeStatus {
		return routeStatus{NodeID: r.NodeID, Key: r.Key(), Port: r.Port, Status: s}
	}
	fail := func(err error) ([]routeStatus, bool) {
		msg := proxyErrorText(err)
		a.Log.Warn("keel-proxy sync failed", "err", msg)
		out := make([]routeStatus, 0, len(routes))
		for _, r := range routes {
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointFailed, Error: msg, At: at}))
		}
		return out, false
	}
	failed := map[string]string{}
	var addrs []string
	if len(routes) > 0 {
		var err error
		if addrs, err = a.Proxy.HostAddrs(ctx); err != nil {
			return fail(err)
		}
		if len(addrs) == 0 {
			return fail(errors.New(MsgNoHostAddress))
		}
	}
	for { // each refused load takes at least one endpoint out, so this ends
		live := make([]ProxyRoute, 0, len(routes))
		for _, r := range routes {
			if _, out := failed[r.Key()]; !out {
				live = append(live, r)
			}
		}
		body, err := json.Marshal(caddyApps(live, addrs, rep, acme))
		if err != nil {
			return fail(err)
		}
		err = a.Proxy.LoadApps(ctx, body)
		if err == nil {
			break
		}
		var rejected *ProxyRejected
		if !errors.As(err, &rejected) {
			return fail(err)
		}
		blamed := blameListener(rejected.Message, live)
		if len(blamed) == 0 {
			return fail(errors.New(rejected.Message))
		}
		for key, why := range blamed {
			failed[key] = why
		}
	}
	var names []string
	for _, r := range routes {
		if _, out := failed[r.Key()]; r.Protocol == domain.ProtocolHTTP && !out {
			names = append(names, r.Domain)
		}
	}
	certs := map[string]ProxyCert{}
	if len(names) > 0 {
		// Any failure here only means "not known yet": the cert event reports it later.
		if got, err := a.Proxy.Certs(ctx, names); err == nil && got != nil {
			certs = got
		}
	}
	out := make([]routeStatus, 0, len(routes))
	for _, r := range routes {
		if why, bad := failed[r.Key()]; bad {
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointFailed, Error: why, At: at}))
			continue
		}
		if r.Protocol != domain.ProtocolHTTP {
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointLive, At: at}))
			continue
		}
		switch cert := certs[r.Domain]; cert.State {
		case "ok":
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointLive, At: at}))
		case "failed":
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointFailed, Error: domain.CertHint(cert.Error, a.Config.PublicIP), At: at}))
		default: // pending or unknown: the cert event reports the outcome
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointStarting, At: at}))
		}
	}
	return out, true
}

var blameRE = regexp.MustCompile(`listen (tcp|udp) \S*?:(\d+): (.+?)(?:$|\n)`)

// blameListener: the endpoints a refused load is about. Caddy loads all or nothing and names the
// listener that would not bind; that endpoint fails alone and the rest load without it. TCP 80
// and 443 belong to every http endpoint. Empty when the error names no live endpoint. §5.1.3.
func blameListener(message string, routes []ProxyRoute) map[string]string {
	m := blameRE.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	protocol, reason := m[1], m[3]
	port, err := strconv.Atoi(m[2])
	if err != nil {
		return nil
	}
	why := "cannot listen on " + strconv.Itoa(port) + "/" + protocol + ": " + reason
	if strings.Contains(reason, "address already in use") {
		why = "port " + strconv.Itoa(port) + "/" + protocol + " is already in use on the control plane"
	}
	why = strings.ToUpper(why[:1]) + why[1:]
	blamed := map[string]string{}
	for _, r := range routes {
		web := protocol == "tcp" && (port == 80 || port == 443) && r.Protocol == domain.ProtocolHTTP
		if web || (string(r.Protocol) == protocol && r.PublicPort == port) {
			blamed[r.Key()] = why
		}
	}
	return blamed
}

// proxyErrorText: whitespace runs collapsed, trimmed, at most 300 characters.
func proxyErrorText(err error) string {
	return domain.TruncateRunes(domain.CollapseSpace(err.Error()), 300)
}

// setEndpointStatuses writes a sync's outcome (§5.3). Only statuses that changed (state or error;
// `at` is ignored) are written, so a resync while all is well costs no write and no
// invalidation. Matching is by key and container port, so an endpoint replaced since the sync
// read it keeps its own status (Q4).
func (a *App) setEndpointStatuses(ctx context.Context, statuses []routeStatus) error {
	if len(statuses) == 0 {
		return nil
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		var order []string
		byNode := map[string]map[string]routeStatus{}
		for _, s := range statuses {
			next := byNode[s.NodeID]
			if next == nil {
				next = map[string]routeStatus{}
				byNode[s.NodeID] = next
				order = append(order, s.NodeID)
			}
			next[s.Key] = s
		}
		for _, id := range order {
			node, err := tx.Node(id)
			if errors.Is(err, ErrNoRow) {
				continue
			}
			if err != nil {
				return err
			}
			eps := append([]domain.Endpoint(nil), node.Endpoints...)
			changed := false
			for i, e := range eps {
				s, ok := byNode[id][e.Key()]
				if ok && s.Port == e.Port && !sameEndpointStatus(s.Status, e.Status) {
					eps[i].Status = s.Status
					changed = true
				}
			}
			if !changed {
				continue
			}
			if err := tx.ReplaceEndpoints(id, eps); err != nil {
				return err
			}
			if err := a.environmentChanged(tx, ch, node.EnvironmentID); err != nil {
				return err
			}
		}
		return nil
	})
}

func sameEndpointStatus(a, b domain.EndpointStatus) bool {
	return a.State == b.State && a.Error == b.Error
}

func (a *App) environmentChanged(tx Tx, ch *Changes, environmentID string) error {
	org, err := tx.OrganizationOfEnvironment(environmentID)
	if errors.Is(err, ErrNoRow) {
		return nil
	}
	if err != nil {
		return err
	}
	ch.Environment(org, environmentID)
	return nil
}

// ReportCert records keel-proxy's certificate report for name on every http endpoint with that
// domain: cert_obtained → live, cert_failed → failed with the next step for the user. A failure is
// final only for this attempt: Caddy keeps retrying and the next cert_obtained flips it. §5.5.
func (a *App) ReportCert(ctx context.Context, event, name, certError string) error {
	var status domain.EndpointStatus
	switch event {
	case CertObtained:
		status = domain.EndpointStatus{State: domain.EndpointLive, At: a.Now()}
	case CertFailed:
		status = domain.EndpointStatus{State: domain.EndpointFailed, Error: domain.CertHint(certError, a.Config.PublicIP), At: a.Now()}
	default:
		return domain.Invalid("bad report")
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		ids, err := tx.IngressNodesWithDomain(name)
		if err != nil {
			return err
		}
		for _, id := range ids {
			node, err := tx.Node(id)
			if errors.Is(err, ErrNoRow) {
				continue
			}
			if err != nil {
				return err
			}
			eps := append([]domain.Endpoint(nil), node.Endpoints...)
			for i, e := range eps {
				if e.Protocol == domain.ProtocolHTTP && e.Domain == name {
					eps[i].Status = status
				}
			}
			if err := tx.ReplaceEndpoints(id, eps); err != nil {
				return err
			}
			if err := a.environmentChanged(tx, ch, node.EnvironmentID); err != nil {
				return err
			}
		}
		if len(ids) > 0 {
			// A sync running now may have read /keel/certs before this certificate existed and would
			// write `starting` over it (Q5): it goes once more when done, and reads it as ok.
			ch.AfterCommit(a.ingress().againIfRunning)
		}
		return nil
	})
}

// recoverIngress is the ingress part of the start-up pass (migrations.run + cron): default
// domains follow a changed public IP, then a sync makes a fresh or restarted proxy serve what the
// database holds, then the resync every 2 minutes.
func (a *App) recoverIngress(ctx context.Context) {
	if ip := a.Config.PublicIP; ip != "" {
		moved, err := a.moveDefaultDomains(ctx, ip)
		if err != nil {
			a.Log.Error("move default domains", "err", err)
		} else if moved > 0 {
			a.Log.Info("default domains moved to the current public IP", "ip", ip, "domainsMoved", moved)
		}
	}
	a.ScheduleProxySync()
	if a.Jobs != nil && a.ingress().resyncArmed.CompareAndSwap(false, true) {
		a.Jobs.Every(proxyResyncName, ProxyResyncInterval, a.ResyncProxy)
	}
}

// moveDefaultDomains: every http endpoint on this node's default sslip.io pattern for another IP
// moves to ip, keeping its name, and starts over (migrations.run step 3). One whose new domain an
// endpoint already holds (the same node exposed again on the current IP, then the IP flipped back)
// stays where it is: domains are unique, and the holder already serves that name.
func (a *App) moveDefaultDomains(ctx context.Context, ip string) (int, error) {
	moved := 0
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		moved = 0
		nodes, err := tx.AllNodes()
		if err != nil {
			return err
		}
		held := map[string]bool{}
		for _, node := range nodes {
			for _, e := range node.Endpoints {
				if e.Protocol == domain.ProtocolHTTP {
					held[e.Domain] = true
				}
			}
		}
		at := a.Now()
		for _, node := range nodes {
			eps := append([]domain.Endpoint(nil), node.Endpoints...)
			changed := false
			for i, e := range eps {
				if e.Protocol != domain.ProtocolHTTP {
					continue
				}
				if d, ok := domain.MovedDefaultDomain(node.ID, e.Domain, ip); ok && !held[d] {
					held[d] = true
					eps[i].Domain = d
					eps[i].Status = domain.EndpointStatus{State: domain.EndpointStarting, At: at}
					changed = true
					moved++
				}
			}
			if !changed {
				continue
			}
			if err := tx.ReplaceEndpoints(node.ID, eps); err != nil {
				return err
			}
			if err := a.environmentChanged(tx, ch, node.EnvironmentID); err != nil {
				return err
			}
		}
		return nil
	})
	return moved, err
}

// ResyncProxy is the 2-minute resync: sync while anything is exposed, or while the last sync
// could not load (so a failed sync after the last unexpose is retried too).
func (a *App) ResyncProxy(ctx context.Context) {
	var exposed bool
	err := a.read(ctx, func(tx Tx) (err error) {
		exposed, err = tx.IngressAnyEndpoint()
		return err
	})
	if err != nil {
		a.Log.Error("keel-proxy resync", "err", err)
		return
	}
	if exposed || a.ingress().failed.Load() {
		a.ScheduleProxySync()
	}
}
