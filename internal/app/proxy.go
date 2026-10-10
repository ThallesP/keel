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

const (
	proxySyncKey         = "proxy:sync"
	proxyResyncName      = "proxy:resync"
	proxyStartupKey      = "proxy:startup"
	proxyStartupAttempts = 8
	ProxyResyncInterval  = 2 * time.Minute
	proxySyncPasses      = 3
	MsgNoHostAddress     = "keel-proxy found no public network address on the control plane"
)

const (
	CertObtained = "cert_obtained"
	CertFailed   = "cert_failed"
)

type ingressState struct {
	mu          sync.Mutex
	running     bool
	again       bool
	failed      atomic.Bool
	resyncArmed atomic.Bool
}

var ingressStates sync.Map

func (a *App) ingress() *ingressState {
	v, _ := ingressStates.LoadOrStore(a, &ingressState{})
	return v.(*ingressState)
}

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

func (st *ingressState) release() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.running, st.again = false, false
}

func (st *ingressState) againIfRunning() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running {
		st.again = true
	}
}

func (a *App) ScheduleProxySync() {
	if a.Jobs == nil {
		return
	}
	a.Jobs.After(proxySyncKey, 0, a.SyncProxy)
}

type proxyReporter struct{ URL, Token string }

type proxyACME struct{ CA, Email string }

type routeStatus struct {
	NodeID string
	Key    string
	Port   int
	Status domain.EndpointStatus
}

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
	for {
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
		if got, err := a.Proxy.Certs(ctx, names); err == nil {
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
		default:
			out = append(out, statusOf(r, domain.EndpointStatus{State: domain.EndpointStarting, At: at}))
		}
	}
	return out, true
}

var blameRE = regexp.MustCompile(`listen (tcp|udp) \S*?:(\d+): (.+?)(?:$|\n)`)

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

func proxyErrorText(err error) string {
	return domain.TruncateRunes(domain.CollapseSpace(err.Error()), 300)
}

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
			ch.AfterCommit(a.ingress().againIfRunning)
		}
		return nil
	})
}

func (a *App) recoverIngress(ctx context.Context) {
	if ip := a.Config.PublicIP; ip != "" {
		moved, err := a.moveDefaultDomains(ctx, ip)
		if err != nil {
			a.Log.Error("move default domains", "err", err)
		} else if moved > 0 {
			a.Log.Info("default domains moved to the current public IP", "ip", ip, "domainsMoved", moved)
		}
	}
	if a.Jobs == nil {
		return
	}
	a.Jobs.After(proxyStartupKey, 0, a.startupProxySync(0))
	if a.ingress().resyncArmed.CompareAndSwap(false, true) {
		a.Jobs.Every(proxyResyncName, ProxyResyncInterval, a.ResyncProxy)
	}
}

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

func (a *App) startupProxySync(attempt int) func(context.Context) {
	return func(ctx context.Context) {
		if a.Proxy != nil && attempt < proxyStartupAttempts && a.anyEndpoint(ctx) {
			if _, err := a.Proxy.HostAddrs(ctx); err != nil {
				delay := min(time.Second<<attempt, 10*time.Second)
				a.Jobs.After(proxyStartupKey, delay, a.startupProxySync(attempt+1))
				return
			}
		}
		a.ScheduleProxySync()
	}
}

func (a *App) anyEndpoint(ctx context.Context) bool {
	var any bool
	err := a.read(ctx, func(tx Tx) (err error) {
		any, err = tx.IngressAnyEndpoint()
		return err
	})
	return err == nil && any
}
