package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	var apps map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.proxy.loads[0]), &apps); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(apps)); !slices.Equal(got, []string{"events", "http", "layer4", "tls"}) {
		t.Fatalf("apps %v in %s", got, e.proxy.loads[0])
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
	if got := igTopics(e.pub.take()); !slices.Equal(got, igEnvTopics) {
		t.Fatalf("published %v", got)
	}

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
		if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: new(d)}); err != nil {
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
	if got := igStatus(e.endpoints(igAPI)); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

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
	if got := igStatus(e.endpoints(igPG)); !slices.Equal(got, []string{"tcp:5432 failed Port 5432/tcp is already in use on the control plane"}) {
		t.Fatalf("pg: %q", got)
	}
	if got := igStatus(e.endpoints(igAPI)); got[0] != "http:api-16w41g.203-0-113-7.sslip.io live " {
		t.Fatalf("api: %q", got)
	}

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
		{"long error cut at 300 runes", func(p *igProxy) {
			p.loadErrs = []error{errors.New(strings.Repeat("é", 400))}
		}, strings.Repeat("é", 300)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newIngressEnv(t)
			igExposeAll(t, e)
			c.setup(e.proxy)
			e.jobs.Run(t)
			got := append(igStatus(e.endpoints(igAPI)), igStatus(e.endpoints(igPG))...)
			want := []string{"http:api-16w41g.203-0-113-7.sslip.io failed " + c.want, "tcp:5432 failed " + c.want}
			if !slices.Equal(got, want) {
				t.Fatalf("got %q\nwant %q", got, want)
			}
		})
	}
}

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
		t.Fatalf("extra: %+v", got)
	}
	if got := e.endpoints(igPG); got[0].Status.State != domain.EndpointLive {
		t.Fatalf("pg: %+v", got)
	}
}

func TestProxyCertReport(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.node("other", igEnvB, domain.NodeService, "shop", "shop:1", 80)
	if _, err := e.expose(e.other, "other", app.ExposeInput{Domain: new("shop.example.com")}); err != nil {
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
	if got := igTopics(e.pub.take()); !slices.Equal(got, igEnvTopics) {
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
	if got := igTopics(e.pub.take()); !slices.Equal(got, []string{"org-b /api/environments/env-b", "org-b /api/nodes/"}) {
		t.Fatalf("published %v", got)
	}

	if err := e.app.ReportCert(e.ctx, app.CertObtained, "api-16w41g.203-0-113-7.sslip.io", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.endpoints(igAPI)[0].Status; got != (domain.EndpointStatus{State: domain.EndpointLive, At: 50_000}) {
		t.Fatalf("got %+v", got)
	}
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

func TestProxyCertReportDuringSync(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	const name = "api-16w41g.203-0-113-7.sslip.io"
	reported := false
	e.proxy.onCerts = func() {
		if reported {
			return
		}
		reported = true
		e.proxy.certs = map[string]app.ProxyCert{name: {State: "ok"}}
		if err := e.app.ReportCert(e.ctx, app.CertObtained, name, ""); err != nil {
			t.Error(err)
		}
		if got := e.endpoints(igAPI)[0].Status.State; got != domain.EndpointLive {
			t.Errorf("report not written: %s", got)
		}
	}
	e.jobs.Run(t)
	if got := e.endpoints(igAPI)[0].Status.State; got != domain.EndpointLive {
		t.Fatalf("a report during a sync was overwritten: %s", got)
	}
	if len(e.proxy.certCalls) != 2 {
		t.Fatalf("certs read %d times, want 2 (the sync, then once more)", len(e.proxy.certCalls))
	}

	if err := e.app.ReportCert(e.ctx, app.CertObtained, name, ""); err != nil {
		t.Fatal(err)
	}
	if e.jobs.Pending("proxy:sync") {
		t.Fatal("a report with no sync running scheduled one")
	}
}

func TestProxySyncCoalescesWhileRunning(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	e.jobs.Run(t)
	before := e.proxy.loadCount()

	entered, release := make(chan struct{}), make(chan struct{})
	e.proxy.onLoad = func(call int) {
		if call == before+1 {
			close(entered)
			<-release
		}
	}
	first := make(chan struct{})
	go func() { e.app.SyncProxy(e.ctx); close(first) }()
	<-entered

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() { defer wg.Done(); e.app.SyncProxy(e.ctx) }()
	}
	returned := make(chan struct{})
	go func() { wg.Wait(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("syncs asked for during a running sync waited for it")
	}
	close(release)
	<-first
	if got := e.proxy.loadCount() - before; got != 2 {
		t.Fatalf("4 syncs asked for during one load pushed %d configs, want 2", got)
	}

	e.app.SyncProxy(e.ctx)
	if got := e.proxy.loadCount() - before; got != 3 {
		t.Fatalf("after the burst: %d loads, want 3", got)
	}
}

func TestProxySyncCancelledFreesTheLoop(t *testing.T) {
	e := newIngressEnv(t)
	igExposeAll(t, e)
	ctx, cancel := context.WithCancel(e.ctx)
	e.proxy.onLoad = func(int) {
		cancel()
		e.app.SyncProxy(e.ctx)
	}
	e.app.SyncProxy(ctx)
	e.proxy.onLoad = nil
	n := e.proxy.loadCount()
	e.app.SyncProxy(e.ctx)
	if got := e.proxy.loadCount(); got != n+1 {
		t.Fatalf("loads %d → %d: the loop stayed taken", n, got)
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
	if got := igTopics(e.pub.take()); !slices.Equal(got, igEnvTopics) {
		t.Fatalf("published %v", got)
	}
	if !e.jobs.Pending("proxy:startup") {
		t.Fatal("no sync at start")
	}
	if e.jobs.every["proxy:resync"] != 2*time.Minute {
		t.Fatalf("resync: %v", e.jobs.every)
	}

	e.pub.take()
	e.app.RecoverIngressForTest(e.ctx)
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("second start published %v", got)
	}
}

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

	if got := igStatus(e.endpoints(igAPI)); !slices.Equal(got, []string{
		"http:api-16w41g.198-51-100-2.sslip.io live ", "http:api-16w41g.203-0-113-7.sslip.io live ",
	}) {
		t.Fatalf("api: %q", got)
	}
	if got := e.endpoints(igPG)[0]; got.Domain != "web-i8r0ew.203-0-113-7.sslip.io" || got.Status.State != domain.EndpointStarting {
		t.Fatalf("web: %+v", got)
	}
}

func TestProxyStartupWaitsForTheProxy(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	live := domain.EndpointStatus{State: domain.EndpointLive, At: 1}
	e.setEndpoints(igAPI, domain.Endpoint{Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io", Status: live})
	e.proxy.addrsErr = errors.New("keel-proxy is not running (no admin socket at /run/keel-proxy/admin.sock)")
	e.app.RecoverIngressForTest(e.ctx)
	for range 3 {
		e.jobs.RunOne(t, "proxy:startup")
	}
	if e.jobs.Pending("proxy:sync") || e.endpoints(igAPI)[0].Status.State != domain.EndpointLive {
		t.Fatalf("synced while the proxy was down: %+v", e.endpoints(igAPI))
	}
	e.proxy.addrsErr = nil
	e.jobs.RunOne(t, "proxy:startup")
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("no sync once the proxy answered")
	}
}
