package app_test

import (
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

const (
	igAPI = "j57a8x2kq3n4m5p6r7s8t9v0w1x2y3z4" // shortHash 16w41g
	igPG  = "k17cs4z0mn2mr6yr7gx8tjqe1n7rw9qj"
	igUDP = "jd7f9g6h5k4m3n2p1q0r9s8t7v6w5x4y"
)

func TestIngressExposeServiceDefaults(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "ghcr.io/acme/api:1", 8080)

	ep, err := e.expose(e.member, igAPI, app.ExposeInput{})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Endpoint{
		NodeID: igAPI, Protocol: domain.ProtocolHTTP, Port: 8080, Domain: "api-16w41g.203-0-113-7.sslip.io",
		Status: domain.EndpointStatus{State: domain.EndpointStarting, At: 1_000},
	}
	if !reflect.DeepEqual(ep, want) {
		t.Fatalf("got %+v\nwant %+v", ep, want)
	}
	stored := e.endpoints(igAPI)
	if len(stored) != 1 || stored[0].Domain != want.Domain || stored[0].PinnedPort {
		t.Fatalf("stored %+v", stored)
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org /api/environments/env"}) {
		t.Fatalf("published %v", got)
	}
	if !e.jobs.Pending("proxy:sync") {
		t.Fatal("no proxy sync scheduled")
	}

	// Exposing what is already exposed returns it: no write, no publication, no sync.
	e.jobs.Run(t)
	e.pub.take()
	e.now = 2_000
	again, err := e.expose(e.member, igAPI, app.ExposeInput{})
	if err != nil || again.Domain != want.Domain || again.Status.At == 2_000 {
		t.Fatalf("again: %+v %v", again, err)
	}
	if got := e.pub.take(); len(got) != 0 || e.jobs.Pending("proxy:sync") {
		t.Fatalf("idempotent expose wrote: %v", got)
	}
}

func TestIngressExposeTCPAllocation(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igPG, igEnvA, domain.NodeDatabase, "postgres", "postgres:16", 5432)
	e.node("pg2", igEnvA, domain.NodeDatabase, "postgres-2", "postgres:16", 5432)
	e.node("pg3", igEnvB, domain.NodeDatabase, "pg", "postgres:16", 5432)

	ep, err := e.expose(e.member, igPG, app.ExposeInput{})
	if err != nil || ep.Protocol != domain.ProtocolTCP || *ep.PublicPort != 5432 || ep.Domain != "" {
		t.Fatalf("first: %+v %v", ep, err)
	}
	// Same container port, taken public port: the first spare one.
	ep2, err := e.expose(e.member, "pg2", app.ExposeInput{})
	if err != nil || *ep2.PublicPort != 20000 {
		t.Fatalf("second: %+v %v", ep2, err)
	}
	// Idempotent shortcut: same protocol and container port.
	ep2b, err := e.expose(e.member, "pg2", app.ExposeInput{})
	if err != nil || *ep2b.PublicPort != 20000 {
		t.Fatalf("again: %+v %v", ep2b, err)
	}
	// Uniqueness is install-wide: another organization's node names the holder.
	_, err = e.expose(e.other, "pg3", app.ExposeInput{PublicPort: igF(5432)})
	igWantErr(t, err, domain.CodeConflict, "Port 5432/tcp is already used by postgres")
	ep3, err := e.expose(e.other, "pg3", app.ExposeInput{})
	if err != nil || *ep3.PublicPort != 20001 {
		t.Fatalf("third: %+v %v", ep3, err)
	}
	// udp ports are their own space, and may use 443.
	ep4, err := e.expose(e.member, igPG, app.ExposeInput{Protocol: domain.ProtocolUDP, PublicPort: igF(443)})
	if err != nil || *ep4.PublicPort != 443 || ep4.Protocol != domain.ProtocolUDP {
		t.Fatalf("udp: %+v %v", ep4, err)
	}
	// A tcp endpoint asking for the container port 443 is moved off the HTTP ports.
	e.node("web", igEnvA, domain.NodeService, "web", "nginx:1", 443)
	ep5, err := e.expose(e.member, "web", app.ExposeInput{Protocol: domain.ProtocolTCP})
	if err != nil || *ep5.PublicPort != 20002 {
		t.Fatalf("tcp 443: %+v %v", ep5, err)
	}
}

func TestIngressExposeErrors(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	e.node("noport", igEnvA, domain.NodeService, "worker", "worker:1", 0, igNoPort)
	e.node("vol", igEnvA, domain.NodeVolume, "data", "", 0, igNoDesired)
	e.node("other-api", igEnvB, domain.NodeService, "shop", "shop:1", 8080)
	if _, err := e.expose(e.other, "other-api", app.ExposeInput{Domain: igS("app.example.com")}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		node    string
		in      app.ExposeInput
		code    string
		message string
	}{
		{"volume", "vol", app.ExposeInput{}, domain.CodeInvalidInput, "Only services, databases and caches can be exposed"},
		{"no port", "noport", app.ExposeInput{}, domain.CodeInvalidInput, "Set the service's port first"},
		{"port 0", igAPI, app.ExposeInput{Port: igF(0)}, domain.CodeInvalidInput, "Port must be 1–65535"},
		{"port 70000", igAPI, app.ExposeInput{Port: igF(70000)}, domain.CodeInvalidInput, "Port must be 1–65535"},
		{"port 80.5", igAPI, app.ExposeInput{Port: igF(80.5)}, domain.CodeInvalidInput, "Port must be 1–65535"},
		{"http with public port", igAPI, app.ExposeInput{PublicPort: igF(8443)}, domain.CodeInvalidInput, "HTTP is always served on 80 and 443"},
		{"bad domain", igAPI, app.ExposeInput{Domain: igS("*.example.com")}, domain.CodeInvalidInput, "Domain must look like app.example.com"},
		{"empty domain", igAPI, app.ExposeInput{Domain: igS("")}, domain.CodeInvalidInput, "Domain must look like app.example.com"},
		{"domain of another org's node", igAPI, app.ExposeInput{Domain: igS("App.Example.com.")}, domain.CodeConflict, "app.example.com is already used by shop"},
		{"tcp with domain", igAPI, app.ExposeInput{Protocol: domain.ProtocolTCP, Domain: igS("a.example.com")}, domain.CodeInvalidInput, "Only HTTP endpoints have a domain"},
		{"tcp on 443", igAPI, app.ExposeInput{Protocol: domain.ProtocolTCP, PublicPort: igF(443)}, domain.CodeInvalidInput, "80 and 443 serve HTTP; pick another public port"},
		{"tcp on 80", igAPI, app.ExposeInput{Protocol: domain.ProtocolTCP, PublicPort: igF(80)}, domain.CodeInvalidInput, "80 and 443 serve HTTP; pick another public port"},
		{"public port 0", igAPI, app.ExposeInput{Protocol: domain.ProtocolTCP, PublicPort: igF(0)}, domain.CodeInvalidInput, "Port must be 1–65535"},
		{"missing node", "nope", app.ExposeInput{}, domain.CodeServiceNotFound, "Node not found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.expose(e.member, c.node, c.in)
			igWantErr(t, err, c.code, c.message)
		})
	}

	e.app.Config.PublicIP = ""
	_, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("api.example.com")})
	igWantErr(t, err, domain.CodeUnavailable, "Keel does not know this server's public IP yet: re-run install.sh, or set KEEL_PUBLIC_IP")
}

func TestIngressExposeRedisGuard(t *testing.T) {
	e := newIngressEnv(t)
	const msg = "Ship this Redis first: its password takes effect on the next Ship"
	e.node("r1", igEnvA, domain.NodeCache, "redis", "redis:7", 6379)
	_, err := e.expose(e.member, "r1", app.ExposeInput{})
	igWantErr(t, err, domain.CodeInvalidInput, msg) // no password

	e.node("r2", igEnvA, domain.NodeCache, "redis-2", "redis:7", 6379, igDirty)
	e.variable("r2", "REDIS_PASSWORD", "x")
	_, err = e.expose(e.member, "r2", app.ExposeInput{})
	igWantErr(t, err, domain.CodeInvalidInput, msg) // password staged, not shipped

	e.node("r3", igEnvA, domain.NodeService, "redis-svc", "docker.io/library/redis:7-alpine", 6379, igUndeployed)
	e.variable("r3", "REDIS_PASSWORD", "x")
	_, err = e.expose(e.member, "r3", app.ExposeInput{})
	igWantErr(t, err, domain.CodeInvalidInput, msg) // any node type, never converged

	e.node("r4", igEnvA, domain.NodeCache, "redis-4", "redis:7", 6379)
	e.variable("r4", "REDIS_PASSWORD", "x")
	ep, err := e.expose(e.member, "r4", app.ExposeInput{})
	if err != nil || ep.Protocol != domain.ProtocolTCP || *ep.PublicPort != 6379 {
		t.Fatalf("shipped redis: %+v %v", ep, err)
	}
}

func TestIngressExposeReplaceAndPin(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)

	// A different container port pins the endpoint; the same key replaces it and moves it last.
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("a.example.com")}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("b.example.com")}); err != nil {
		t.Fatal(err)
	}
	ep, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("a.example.com"), Port: igF(9090)})
	if err != nil || ep.Port != 9090 || !ep.PinnedPort {
		t.Fatalf("pinned: %+v %v", ep, err)
	}
	eps := e.endpoints(igAPI)
	if len(eps) != 2 || eps[0].Domain != "b.example.com" || eps[1].Domain != "a.example.com" || !eps[1].PinnedPort {
		t.Fatalf("after replace: %+v", eps)
	}
	// Re-exposing with the node's own port un-pins.
	ep, err = e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("a.example.com"), Port: igF(8080)})
	if err != nil || ep.PinnedPort {
		t.Fatalf("unpinned: %+v %v", ep, err)
	}
}

func TestIngressExposeLimit(t *testing.T) {
	e := newIngressEnv(t)
	e.node("game", igEnvA, domain.NodeService, "game", "game:1", 27015)
	for p := 27015; p < 27025; p++ {
		if _, err := e.expose(e.member, "game", app.ExposeInput{Protocol: domain.ProtocolUDP, PublicPort: igF(float64(p))}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := e.expose(e.member, "game", app.ExposeInput{Protocol: domain.ProtocolUDP, PublicPort: igF(27025)})
	igWantErr(t, err, domain.CodeInvalidInput, "At most 10 endpoints per node")
	// Replacing one of the 10 is allowed.
	ep, err := e.expose(e.member, "game", app.ExposeInput{Protocol: domain.ProtocolUDP, PublicPort: igF(27015), Port: igF(27016)})
	if err != nil || ep.Port != 27016 || len(e.endpoints("game")) != 10 {
		t.Fatalf("replace at the limit: %+v %v", ep, err)
	}
}

func TestIngressUnexpose(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	for _, in := range []app.ExposeInput{
		{},
		{Domain: igS("app.example.com")},
		{Protocol: domain.ProtocolTCP, PublicPort: igF(8080)},
	} {
		if _, err := e.expose(e.member, igAPI, in); err != nil {
			t.Fatal(err)
		}
	}
	e.jobs.Run(t)
	e.pub.take()

	partial := []app.UnexposeInput{
		{Protocol: domain.ProtocolHTTP},
		{Protocol: domain.ProtocolTCP},
		{Protocol: domain.ProtocolTCP, Domain: igS("app.example.com")},
		{Domain: igS("app.example.com")},
		{PublicPort: igF(8080)},
	}
	for _, in := range partial {
		igWantErr(t, e.app.Unexpose(e.ctx, e.member, igAPI, in), domain.CodeInvalidInput,
			"Name the endpoint: protocol and domain (http) or public port")
	}
	// The domain is validated whenever a protocol is named, even for tcp.
	igWantErr(t, e.app.Unexpose(e.ctx, e.member, igAPI, app.UnexposeInput{Protocol: domain.ProtocolTCP, Domain: igS("bad_domain"), PublicPort: igF(1)}),
		domain.CodeInvalidInput, "Domain must look like app.example.com")

	// Unknown endpoints and non-integer ports are no-ops.
	for _, in := range []app.UnexposeInput{
		{Protocol: domain.ProtocolHTTP, Domain: igS("nope.example.com")},
		{Protocol: domain.ProtocolHTTP, Domain: igS("")},
		{Protocol: domain.ProtocolTCP, PublicPort: igF(8080.5)},
		{Protocol: domain.ProtocolUDP, PublicPort: igF(8080)},
	} {
		if err := e.app.Unexpose(e.ctx, e.member, igAPI, in); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.pub.take(); len(got) != 0 || len(e.endpoints(igAPI)) != 3 || e.jobs.Pending("proxy:sync") {
		t.Fatalf("no-op unexpose wrote: %v", got)
	}

	// By domain (normalized), then by public port.
	if err := e.app.Unexpose(e.ctx, e.member, igAPI, app.UnexposeInput{Protocol: domain.ProtocolHTTP, Domain: igS(" APP.example.com. ")}); err != nil {
		t.Fatal(err)
	}
	eps := e.endpoints(igAPI)
	if len(eps) != 2 || eps[0].Domain != "api-16w41g.203-0-113-7.sslip.io" || eps[1].Protocol != domain.ProtocolTCP {
		t.Fatalf("after domain unexpose: %+v", eps)
	}
	if !e.jobs.Pending("proxy:sync") || len(e.pub.take()) != 1 {
		t.Fatal("unexpose did not sync or publish")
	}
	if err := e.app.Unexpose(e.ctx, e.member, igAPI, app.UnexposeInput{Protocol: domain.ProtocolTCP, PublicPort: igF(8080)}); err != nil {
		t.Fatal(err)
	}
	if eps := e.endpoints(igAPI); len(eps) != 1 {
		t.Fatalf("after port unexpose: %+v", eps)
	}
	// No selector: Make private.
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("b.example.com")}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Unexpose(e.ctx, e.member, igAPI, app.UnexposeInput{}); err != nil {
		t.Fatal(err)
	}
	if eps := e.endpoints(igAPI); len(eps) != 0 {
		t.Fatalf("after make private: %+v", eps)
	}
	// Nothing exposed: a no-op, even for a named endpoint.
	e.pub.take()
	if err := e.app.Unexpose(e.ctx, e.member, igAPI, app.UnexposeInput{Protocol: domain.ProtocolTCP, PublicPort: igF(1)}); err != nil {
		t.Fatal(err)
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

// A member of another organization can neither read nor change this organization's endpoints:
// its node is "Node not found", exactly like a missing one.
func TestIngressForeignOrganization(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{}); err != nil {
		t.Fatal(err)
	}
	e.pub.take()

	_, err := e.expose(e.other, igAPI, app.ExposeInput{Domain: igS("evil.example.com")})
	igWantErr(t, err, domain.CodeServiceNotFound, "Node not found")
	igWantErr(t, e.app.Unexpose(e.ctx, e.other, igAPI, app.UnexposeInput{}), domain.CodeServiceNotFound, "Node not found")
	noOrg := domain.Actor{UserID: "u3"}
	igWantErr(t, e.app.Unexpose(e.ctx, noOrg, igAPI, app.UnexposeInput{}), domain.CodeServiceNotFound, "Node not found")
	igWantErr(t, e.app.Unexpose(e.ctx, domain.Actor{}, igAPI, app.UnexposeInput{}), domain.CodeNotAuthenticated, "Not authenticated")

	if eps := e.endpoints(igAPI); len(eps) != 1 || eps[0].Domain != "api-16w41g.203-0-113-7.sslip.io" {
		t.Fatalf("foreign caller changed endpoints: %+v", eps)
	}
	if got := e.pub.take(); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

func TestIngressControlPlane(t *testing.T) {
	e := newIngressEnv(t)
	if _, err := e.app.ControlPlanePublicIP(domain.Actor{}); err == nil {
		t.Fatal("signed out read the public IP")
	}
	ip, err := e.app.ControlPlanePublicIP(domain.Actor{UserID: "u3"}) // no organization needed
	if err != nil || ip != igIP {
		t.Fatalf("got %q %v", ip, err)
	}
}

func TestIngressFollowPort(t *testing.T) {
	e := newIngressEnv(t)
	e.node(igAPI, igEnvA, domain.NodeService, "api", "api:1", 8080)
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.expose(e.member, igAPI, app.ExposeInput{Domain: igS("admin.example.com"), Port: igF(9000)}); err != nil {
		t.Fatal(err)
	}
	e.setEndpoints(igAPI, func() []domain.Endpoint {
		eps := e.endpoints(igAPI)
		for i := range eps {
			eps[i].Status = domain.EndpointStatus{State: domain.EndpointLive, At: 1}
		}
		return eps
	}()...)
	e.pub.take()
	e.now = 5_000

	moved, err := e.app.FollowPortForTest(e.ctx, igAPI, 3000)
	if err != nil || !moved {
		t.Fatalf("moved=%v err=%v", moved, err)
	}
	eps := e.endpoints(igAPI)
	if eps[0].Port != 3000 || eps[0].Status != (domain.EndpointStatus{State: domain.EndpointStarting, At: 5_000}) {
		t.Fatalf("unpinned endpoint: %+v", eps[0])
	}
	if eps[1].Port != 9000 || eps[1].Status.State != domain.EndpointLive {
		t.Fatalf("pinned endpoint moved: %+v", eps[1])
	}
	if got := igTopics(e.pub.take()); !reflect.DeepEqual(got, []string{"org /api/environments/env"}) {
		t.Fatalf("published %v", got)
	}
	// Nothing to move: no write.
	moved, err = e.app.FollowPortForTest(e.ctx, igAPI, 3000)
	if err != nil || moved || len(e.pub.take()) != 0 {
		t.Fatalf("second follow: moved=%v err=%v", moved, err)
	}
}
