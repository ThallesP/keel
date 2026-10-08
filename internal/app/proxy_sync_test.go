package app_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func igStatus(eps []domain.Endpoint) []string {
	var out []string
	for _, e := range eps {
		out = append(out, e.Key()+" "+string(e.Status.State)+" "+e.Status.Error)
	}
	return out
}

// igExposeAll: an http and a tcp endpoint on igAPI / igPG, synced once.
func igExposeAll(t *testing.T, e *igEnv) {
	t.Helper()
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	e.node(igPG, igEnvA, domain.NodeDatabase, "postgres", "postgres:16", 5432)
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.expose(e.member, igPG, app.ExposeInput{}); err != nil {
		t.Fatal(err)
	}
	e.pub.take()
}

func TestProxySyncLoadsAndRecordsStatuses(t *testing.T) {
	e := newIngressEnv(t)
	e.proxy.addrs = []string{igIP, "2001:db8::1"}
	e.proxy.reportURL = "http://100.64.0.1:8080/proxy/events"
	e.app.Config.ACMEEmail = "ops@example.com"
	igExposeAll(t, e)
	e.proxy.certs = map[string]app.ProxyCert{"api-16w41g.203-0-113-7.sslip.io": {State: "ok"}}
	e.now = 7_000
	e.jobs.Run(t)

	if len(e.proxy.loads) != 1 {
		t.Fatalf("loads: %d", len(e.proxy.loads))
	}
	var apps map[string]any
	if err := json.Unmarshal([]byte(e.proxy.loads[0]), &apps); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"http", "tls", "events", "layer4"} {
		if apps[app] == nil {
			t.Fatalf("no %s app in %s", app, e.proxy.loads[0])
		}
	}
	for _, want := range []string{
		`"listen":["host-tcp/203.0.113.7:443","host-tcp/[2001:db8::1]:443"]`,
		`"dial":"svc-` + igAPI + `:8080"`,
		`"tcp-5432"`,
		`"url":"http://100.64.0.1:8080/proxy/events"`,
		`"token":"tok"`,
		`"subjects":["api-16w41g.203-0-113-7.sslip.io"]`,
	} {
		if !strings.Contains(e.proxy.loads[0], want) {
			t.Errorf("config lacks %s:\n%s", want, e.proxy.loads[0])
		}
	}
	if !reflect.DeepEqual(e.proxy.certCalls, [][]string{{"api-16w41g.203-0-113-7.sslip.io"}}) {
		t.Fatalf("certs asked for %v", e.proxy.certCalls)
	}
	api, pg := e.endpoints(igAPI), e.endpoints(igPG)
	if api[0].Status != (domain.EndpointStatus{State: domain.EndpointLive, At: 7_000}) || pg[0].Status.State != domain.EndpointLive {
		t.Fatalf("statuses: %+v %+v", api, pg)
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org /api/environments/env"}) {
		t.Fatalf("published %v", got)
	}

	// Nothing changed: the resync loads the same bytes and writes nothing.
	e.now = 9_000
	e.app.ResyncProxy(e.ctx)
	e.jobs.Run(t)
	if len(e.proxy.loads) != 2 || e.proxy.loads[1] != e.proxy.loads[0] {
		t.Fatalf("resync config differs:\n%s\n%s", e.proxy.loads[0], e.proxy.loads[len(e.proxy.loads)-1])
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("unchanged statuses published %v", got)
	}
	if e.endpoints(igAPI)[0].Status.At != 7_000 {
		t.Fatal("unchanged status rewritten")
	}
}

func TestProxySyncDefaultReportURL(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.jobs.Run(t)
	if !strings.Contains(e.proxy.loads[0], `"url":"https://keel.example.ts.net/proxy/events"`) {
		t.Fatalf("report URL: %s", e.proxy.loads[0])
	}
}

func TestProxySyncCertStates(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	for _, d := range []string{"ok.example.com", "bad.example.com", "wait.example.com"} {
		if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS(d)}); err != nil {
			t.Fatal(err)
		}
	}
	e.proxy.certs = map[string]app.ProxyCert{
		"ok.example.com":   {State: "ok"},
		"bad.example.com":  {State: "failed", Error: "HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN looking up A for bad.example.com"},
		"wait.example.com": {State: "pending"},
	}
	e.jobs.Run(t)
	want := []string{
		"http:ok.example.com live ",
		"http:bad.example.com failed DNS problem: NXDOMAIN looking up A for bad.example.com. Point the domain at 203.0.113.7 with an A record.",
		"http:wait.example.com starting ",
	}
	if got := igStatus(e.endpoints(igAPI)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	// /keel/certs failing only means "not known yet".
	e.proxy.certsErr = errors.New("keel-proxy did not answer within 30s")
	e.app.ScheduleProxySync()
	e.jobs.Run(t)
	for _, s := range igStatus(e.endpoints(igAPI)) {
		if !strings.HasSuffix(s, "starting ") {
			t.Fatalf("after certs error: %q", s)
		}
	}
}

func TestProxySyncBlamesTheListener(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.proxy.certs = map[string]app.ProxyCert{"api-16w41g.203-0-113-7.sslip.io": {State: "ok"}}
	e.proxy.loadErrs = []error{&app.ProxyRejected{Message: "layer4 app module: start: listening on host-tcp/203.0.113.7:5432: listen tcp 203.0.113.7:5432: bind: address already in use"}}
	e.jobs.Run(t)

	if len(e.proxy.loads) != 2 || !strings.Contains(e.proxy.loads[0], "layer4") || strings.Contains(e.proxy.loads[1], "layer4") {
		t.Fatalf("loads: %q", e.proxy.loads)
	}
	if got := igStatus(e.endpoints(igPG)); !reflect.DeepEqual(got, []string{"tcp:5432 failed Port 5432/tcp is already in use on the control plane"}) {
		t.Fatalf("pg: %q", got)
	}
	if got := igStatus(e.endpoints(igAPI)); got[0] != "http:api-16w41g.203-0-113-7.sslip.io live " {
		t.Fatalf("api: %q", got)
	}

	// 443 taken: every http endpoint fails, tcp still loads.
	e.proxy.loadErrs = []error{&app.ProxyRejected{Message: "http app module: start: listening on host-tcp/[2001:db8::1]:443: listen tcp [2001:db8::1]:443: bind: address already in use"}}
	e.app.ScheduleProxySync()
	e.jobs.Run(t)
	if got := igStatus(e.endpoints(igAPI)); got[0] != "http:api-16w41g.203-0-113-7.sslip.io failed Port 443/tcp is already in use on the control plane" {
		t.Fatalf("api: %q", got)
	}
	if got := igStatus(e.endpoints(igPG)); got[0] != "tcp:5432 live " {
		t.Fatalf("pg: %q", got)
	}
}

func TestProxySyncWholeFailure(t *testing.T) {
	cases := []struct {
		name  string
		setup func(p *igProxy)
		want  string
	}{
		{"not running", func(p *igProxy) {
			p.addrsErr = errors.New("keel-proxy is not running (no admin socket at /run/keel-proxy/admin.sock)")
		}, "keel-proxy is not running (no admin socket at /run/keel-proxy/admin.sock)"},
		{"no host address", func(p *igProxy) { p.addrs = nil }, "keel-proxy found no public network address on the control plane"},
		{"unattributable rejection", func(p *igProxy) {
			p.loadErrs = []error{&app.ProxyRejected{Message: "json: cannot unmarshal\n  something   odd"}}
		}, "json: cannot unmarshal something odd"},
		{"rejection naming nobody live", func(p *igProxy) {
			p.loadErrs = []error{&app.ProxyRejected{Message: "listen tcp 203.0.113.7:6379: bind: address already in use"}}
		}, "listen tcp 203.0.113.7:6379: bind: address already in use"},
		{"proxy unreachable mid-load", func(p *igProxy) {
			p.loadErrs = []error{errors.New("keel-proxy did not answer within 30s")}
		}, "keel-proxy did not answer within 30s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newIngressEnv(t)
			igExposeAll(t, e)
			c.setup(e.proxy)
			e.jobs.Run(t)
			got := append(igStatus(e.endpoints(igAPI)), igStatus(e.endpoints(igPG))...)
			want := []string{"http:api-16w41g.203-0-113-7.sslip.io failed " + c.want, "tcp:5432 failed " + c.want}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q\nwant %q", got, want)
			}
		})
	}
}

// After the last unexpose, a sync loads `{}`; when that fails, the resync keeps trying even
// though nothing is exposed any more.
func TestProxyResync(t *testing.T) {
	e := newIngressEnv(t)
	e.app.ResyncProxy(e.ctx)
	if e.jobs.Pending("proxy:sync") {
		t.Fatal("resync with nothing exposed")
	}
	igExposeAll(t, e)
	e.jobs.Run(t)
	e.app.ResyncProxy(e.ctx)
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("no resync while exposed")
	}
	e.jobs.Run(t)

	for _, id := range []string{igAPI, igPG} {
		if err := e.app.Unexpose(e.ctx, e.member, id, app.UnexposeInput{}); err != nil {
			t.Fatal(err)
		}
	}
	e.proxy.loadErrs = []error{errors.New("keel-proxy is not running (no admin socket at /x)")}
	e.jobs.Run(t)
	if last := e.proxy.loads[len(e.proxy.loads)-1]; last != "{}" {
		t.Fatalf("empty config: %s", last)
	}
	e.app.ResyncProxy(e.ctx)
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("failed sync not retried")
	}
	e.jobs.Run(t)
	e.app.ResyncProxy(e.ctx)
	if e.jobs.Pending("proxy:sync") {
		t.Fatal("resync after a good empty load")
	}
}

// Endpoints changed while the config loaded: the sync goes again with the new set.
func TestProxySyncGoesAgainWhenEndpointsMove(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.node("extra", igEnvA, domain.NodeService, "extra", "extra:1", 3000)
	e.proxy.onLoad = func(call int) {
		if call == 1 {
			if _, err := e.expose(e.member, "extra", app.ExposeInput{}); err != nil {
				t.Error(err)
			}
		}
	}
	e.jobs.Run(t)
	if len(e.proxy.loads) < 2 || strings.Contains(e.proxy.loads[0], "svc-extra") || !strings.Contains(e.proxy.loads[1], "svc-extra") {
		t.Fatalf("loads: %q", e.proxy.loads)
	}
	if got := e.endpoints("extra"); got[0].Status.State != domain.EndpointStarting {
		t.Fatalf("extra: %+v", got) // http, cert pending
	}
	if got := e.endpoints(igPG); got[0].Status.State != domain.EndpointLive {
		t.Fatalf("pg: %+v", got)
	}
}

func TestProxyCertReport(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.node("other", igEnvB, domain.NodeService, "shop", "shop:1", 80)
	if _, err := e.expose(e.other, "other", app.ExposeInput{Domain: igS("shop.example.com")}); err != nil {
		t.Fatal(err)
	}
	e.jobs.Run(t)
	e.pub.take()
	e.now = 50_000

	if err := e.app.ReportCert(e.ctx, app.CertFailed, "api-16w41g.203-0-113-7.sslip.io",
		"HTTP 400 urn:ietf:params:acme:error:connection - 203.0.113.7: Timeout during connect (likely firewall problem)"); err != nil {
		t.Fatal(err)
	}
	want := domain.EndpointStatus{State: domain.EndpointFailed, At: 50_000,
		Error: "Open ports 80 and 443 on the control plane to the internet: the certificate authority could not connect (203.0.113.7: Timeout during connect)"}
	if got := e.endpoints(igAPI)[0].Status; got != want {
		t.Fatalf("got %+v", got)
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org /api/environments/env"}) {
		t.Fatalf("published %v", got)
	}
	if e.endpoints("other")[0].Status.State == domain.EndpointFailed {
		t.Fatal("report touched another domain")
	}

	if err := e.app.ReportCert(e.ctx, app.CertFailed, "shop.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.endpoints("other")[0].Status.Error; got != "Could not get a certificate" {
		t.Fatalf("no error text: %q", got)
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org-b /api/environments/env-b"}) {
		t.Fatalf("published %v", got)
	}

	if err := e.app.ReportCert(e.ctx, app.CertObtained, "api-16w41g.203-0-113-7.sslip.io", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.endpoints(igAPI)[0].Status; got != (domain.EndpointStatus{State: domain.EndpointLive, At: 50_000}) {
		t.Fatalf("got %+v", got)
	}
	// Unknown names are ignored; no sync is scheduled when no sync is running.
	e.pub.take()
	if err := e.app.ReportCert(e.ctx, app.CertObtained, "nobody.example.com", ""); err != nil {
		t.Fatal(err)
	}
	if len(e.pub.take()) != 0 || e.jobs.Pending("proxy:sync") {
		t.Fatal("unknown name wrote or synced")
	}
	if err := e.app.ReportCert(e.ctx, "cert_renewed", "x", ""); err == nil {
		t.Fatal("unknown event accepted")
	}
}

// A report that lands while a sync runs (which may have read /keel/certs before the
// certificate existed) is followed by another sync (Q5).
func TestProxyCertReportDuringSync(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.jobs.Run(t)
	release := e.app.HoldProxySyncForTest()
	if err := e.app.ReportCert(e.ctx, app.CertObtained, "api-16w41g.203-0-113-7.sslip.io", ""); err != nil {
		t.Fatal(err)
	}
	release()
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("no sync after a report during a sync")
	}
}

func TestProxyRecover(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	e.setEndpoints(igAPI,
		domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.198-51-100-2.sslip.io", Status: domain.EndpointStatus{State: domain.EndpointLive, At: 1}},
		domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "app.example.com", Status: domain.EndpointStatus{State: domain.EndpointLive, At: 1}},
	)
	e.now = 3_000
	e.app.RecoverIngressForTest(e.ctx)

	eps := e.endpoints(igAPI)
	if eps[0].Domain != "api-16w41g.203-0-113-7.sslip.io" || eps[0].Status != (domain.EndpointStatus{State: domain.EndpointStarting, At: 3_000}) {
		t.Fatalf("default domain: %+v", eps[0])
	}
	if eps[1].Domain != "app.example.com" || eps[1].Status.State != domain.EndpointLive {
		t.Fatalf("custom domain: %+v", eps[1])
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org /api/environments/env"}) {
		t.Fatalf("published %v", got)
	}
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("no sync at start")
	}
	if e.jobs.every["proxy:resync"] != 2*time.Minute {
		t.Fatalf("resync: %v", e.jobs.every)
	}

	// Run again: nothing left to move, nothing written.
	e.pub.take()
	e.app.RecoverIngressForTest(e.ctx)
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("second start published %v", got)
	}
}

// The IP flipped back after the node was exposed again on the other IP: the old endpoint cannot
// take a name its sibling already serves, and the rest of the migration still runs.
func TestProxyRecoverKeepsDomainsUnique(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	e.node(igPG, igEnvA, domain.NodeService, "web", "web:1", 8080)
	live := domain.EndpointStatus{State: domain.EndpointLive, At: 1}
	e.setEndpoints(igAPI,
		domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.198-51-100-2.sslip.io", Status: live},
		domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io", Status: live},
	)
	e.setEndpoints(igPG, domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "web-i8r0ew.198-51-100-2.sslip.io", Status: live})
	e.app.RecoverIngressForTest(e.ctx)

	if got := igStatus(e.endpoints(igAPI)); !reflect.DeepEqual(got, []string{
		"http:api-16w41g.198-51-100-2.sslip.io live ", "http:api-16w41g.203-0-113-7.sslip.io live ",
	}) {
		t.Fatalf("api: %q", got)
	}
	if got := e.endpoints(igPG)[0]; got.Domain != "web-i8r0ew.203-0-113-7.sslip.io" || got.Status.State != domain.EndpointStarting {
		t.Fatalf("web: %+v", got)
	}
}
