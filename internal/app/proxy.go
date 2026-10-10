package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const proxyStartupKey = "proxy:startup"

const (
	CertObtained = "cert_obtained"
	CertFailed   = "cert_failed"
)

type ingressState struct {
	mu      sync.Mutex
	running bool
	again   bool
	failed  atomic.Bool
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
	a.Jobs.After("proxy:sync", 0, a.SyncProxy)
}

type routeStatus struct {
	ProxyRoute
	Status domain.EndpointStatus
}

func (a *App) SyncProxy(ctx context.Context) {
	if !a.ingress.begin() {
		return
	}
	finished := false
	defer func() {
		if !finished {
			a.ingress.release()
		}
	}()
	for ctx.Err() == nil {
		a.syncProxy(ctx)
		if !a.ingress.next() {
			finished = true
			return
		}
	}
}

func (a *App) syncProxy(ctx context.Context) {
	for range 3 {
		routes, err := a.proxyRoutes(ctx)
		if err != nil {
			a.Log.Error("keel-proxy sync: read endpoints", "err", err)
			return
		}
		statuses, loaded := a.applyProxy(ctx, routes)
		a.ingress.failed.Store(!loaded)
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

func (a *App) applyProxy(ctx context.Context, routes []ProxyRoute) ([]routeStatus, bool) {
	at := a.Now()
	out := make([]routeStatus, 0, len(routes))
	failed, err := a.loadProxy(ctx, routes)
	if err != nil {
		msg := compactText(err.Error(), 300)
		a.Log.Warn("keel-proxy sync failed", "err", msg)
		for _, r := range routes {
			out = append(out, routeStatus{r, domain.EndpointStatus{State: domain.EndpointFailed, Error: msg, At: at}})
		}
		return out, false
	}
	var names []string
	for _, r := range routes {
		if _, blamed := failed[r.Key()]; r.Protocol == domain.ProtocolHTTP && !blamed {
			names = append(names, r.Domain)
		}
	}
	var certs map[string]ProxyCert
	if len(names) > 0 {
		if got, err := a.Proxy.Certs(ctx, names); err == nil {
			certs = got
		}
	}
	for _, r := range routes {
		status := domain.EndpointStatus{State: domain.EndpointLive, At: at}
		cert := certs[r.Domain]
		why, blamed := failed[r.Key()]
		switch {
		case blamed:
			status.State, status.Error = domain.EndpointFailed, why
		case r.Protocol != domain.ProtocolHTTP, cert.State == "ok":
		case cert.State == "failed":
			status.State, status.Error = domain.EndpointFailed, domain.CertHint(cert.Error, a.Config.PublicIP)
		default:
			status.State = domain.EndpointStarting
		}
		out = append(out, routeStatus{r, status})
	}
	return out, true
}

func (a *App) loadProxy(ctx context.Context, routes []ProxyRoute) (map[string]string, error) {
	var addrs []string
	if len(routes) > 0 {
		var err error
		if addrs, err = a.Proxy.HostAddrs(ctx); err != nil {
			return nil, err
		}
		if len(addrs) == 0 {
			return nil, errors.New("keel-proxy found no public network address on the control plane")
		}
	}
	reportURL := cmp.Or(a.Proxy.ReportURL(), a.Config.SiteURL+"/proxy/events")
	failed := map[string]string{}
	live := routes
	for {
		body, _ := json.Marshal(caddyApps(live, addrs, reportURL, a.Config))
		err := a.Proxy.LoadApps(ctx, body)
		if err == nil {
			return failed, nil
		}
		var rejected *ProxyRejected
		if !errors.As(err, &rejected) {
			return nil, err
		}
		blamed := blameListener(rejected.Message, live)
		if len(blamed) == 0 {
			return nil, err
		}
		maps.Copy(failed, blamed)
		live = slices.DeleteFunc(slices.Clone(live), func(r ProxyRoute) bool {
			_, ok := blamed[r.Key()]
			return ok
		})
	}
}

var blameRE = regexp.MustCompile(`listen (tcp|udp) \S*?:(\d+): (.+)`)

func blameListener(message string, routes []ProxyRoute) map[string]string {
	m := blameRE.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	protocol, reason := m[1], m[3]
	port, _ := strconv.Atoi(m[2])
	why := fmt.Sprintf("Cannot listen on %d/%s: %s", port, protocol, reason)
	if strings.Contains(reason, "address already in use") {
		why = fmt.Sprintf("Port %d/%s is already in use on the control plane", port, protocol)
	}
	blamed := map[string]string{}
	for _, r := range routes {
		web := protocol == "tcp" && domain.IsHTTPPort(port) && r.Protocol == domain.ProtocolHTTP
		if web || (string(r.Protocol) == protocol && r.PublicPort == port) {
			blamed[r.Key()] = why
		}
	}
	return blamed
}

func (a *App) setEndpointStatuses(ctx context.Context, statuses []routeStatus) error {
	if len(statuses) == 0 {
		return nil
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		byNode := map[string]map[string]routeStatus{}
		for _, s := range statuses {
			if byNode[s.NodeID] == nil {
				byNode[s.NodeID] = map[string]routeStatus{}
			}
			byNode[s.NodeID][s.Key()] = s
		}
		for id, byKey := range byNode {
			node, err := tx.Node(id)
			if errors.Is(err, ErrNoRow) {
				continue
			}
			if err != nil {
				return err
			}
			changed := false
			for i, e := range node.Endpoints {
				s, ok := byKey[e.Key()]
				if !ok || s.Port != e.Port || (s.Status.State == e.Status.State && s.Status.Error == e.Status.Error) {
					continue
				}
				node.Endpoints[i].Status = s.Status
				changed = true
			}
			if !changed {
				continue
			}
			if err := tx.ReplaceEndpoints(id, node.Endpoints); err != nil {
				return err
			}
			if err := environmentChanged(tx, ch, node.EnvironmentID); err != nil {
				return err
			}
		}
		return nil
	})
}

func environmentChanged(tx Tx, ch *Changes, environmentID string) error {
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
		id, err := tx.IngressNodeWithDomain(name)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		node, err := tx.Node(id)
		if err != nil {
			return err
		}
		for i, e := range node.Endpoints {
			if e.Domain == name {
				node.Endpoints[i].Status = status
			}
		}
		if err := tx.ReplaceEndpoints(id, node.Endpoints); err != nil {
			return err
		}
		ch.AfterCommit(a.ingress.againIfRunning)
		return environmentChanged(tx, ch, node.EnvironmentID)
	})
}

func (a *App) recoverIngress(ctx context.Context) {
	if ip := a.Config.PublicIP; ip != "" {
		if err := a.moveDefaultDomains(ctx, ip); err != nil {
			a.Log.Error("move default domains", "err", err)
		}
	}
	a.Jobs.After(proxyStartupKey, 0, a.startupProxySync(0))
	a.Jobs.Every("proxy:resync", 2*time.Minute, a.ResyncProxy)
}

func (a *App) moveDefaultDomains(ctx context.Context, ip string) error {
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		nodes, err := tx.AllNodes()
		if err != nil {
			return err
		}
		held := map[string]bool{}
		for _, node := range nodes {
			for _, e := range node.Endpoints {
				held[e.Domain] = true
			}
		}
		at := a.Now()
		for _, node := range nodes {
			changed := false
			for i, e := range node.Endpoints {
				d, ok := domain.MovedDefaultDomain(node.ID, e.Domain, ip)
				if !ok || held[d] {
					continue
				}
				held[d] = true
				node.Endpoints[i].Domain = d
				node.Endpoints[i].Status = domain.EndpointStatus{State: domain.EndpointStarting, At: at}
				changed = true
			}
			if !changed {
				continue
			}
			if err := tx.ReplaceEndpoints(node.ID, node.Endpoints); err != nil {
				return err
			}
			if err := environmentChanged(tx, ch, node.EnvironmentID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) ResyncProxy(ctx context.Context) {
	exposed, err := a.anyEndpoint(ctx)
	if err != nil {
		a.Log.Error("keel-proxy resync", "err", err)
		return
	}
	if exposed || a.ingress.failed.Load() {
		a.ScheduleProxySync()
	}
}

func (a *App) startupProxySync(attempt int) func(context.Context) {
	return func(ctx context.Context) {
		exposed, _ := a.anyEndpoint(ctx)
		if attempt < 8 && exposed {
			if _, err := a.Proxy.HostAddrs(ctx); err != nil {
				delay := min(time.Second<<attempt, 10*time.Second)
				a.Jobs.After(proxyStartupKey, delay, a.startupProxySync(attempt+1))
				return
			}
		}
		a.ScheduleProxySync()
	}
}

func (a *App) anyEndpoint(ctx context.Context) (bool, error) {
	var exposed bool
	err := a.read(ctx, func(tx Tx) (err error) {
		exposed, err = tx.IngressAnyEndpoint()
		return err
	})
	return exposed, err
}
