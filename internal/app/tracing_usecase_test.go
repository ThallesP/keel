package app_test

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

var obsTracesOn = domain.LogSink{Kind: domain.SinkKindAxiom, Domain: "api.axiom.co", Dataset: "keel-logs", Traces: "keel-traces", Token: "xaat-abcdefgh"}

func obsOTLPKeyOf(t *testing.T, e *obsEnv, env string) string {
	t.Helper()
	var key string
	_ = e.store.DB().QueryRow(`SELECT key FROM otlp_keys WHERE environment_id = ?`, env).Scan(&key)
	return key
}

func TestSetNodeTracing(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)

	obsWantCode(t, e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true), domain.CodeTracesOff, "Connect Axiom to see traces")
	old := obsTracesOn
	old.Traces = ""
	e.setSink(t, "org", old)
	obsWantCode(t, e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true), domain.CodeTracesOff, "Sign in with Axiom again to turn on traces")
	if n := e.node(t, obsNodeAPI); n.Desired.Tracing || n.Dirty || obsOTLPKeyOf(t, e, "env") != "" {
		t.Fatalf("refused switch changed something: %+v key=%q", n, obsOTLPKeyOf(t, e, "env"))
	}
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, false); err != nil {
		t.Fatal(err)
	}
	if n := e.node(t, obsNodeAPI); n.Dirty {
		t.Fatal("an unchanged switch marked the node dirty")
	}

	e.setSink(t, "org", obsTracesOn)
	e.pub.take("org")
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true); err != nil {
		t.Fatal(err)
	}
	n := e.node(t, obsNodeAPI)
	if !n.Desired.Tracing || !n.Dirty || n.Desired.Revision != 1 {
		t.Fatalf("after enable: %+v", n.Desired)
	}
	key := obsOTLPKeyOf(t, e, "env")
	if !regexp.MustCompile(`^keel_otlp_[A-Za-z0-9_-]{32}$`).MatchString(key) {
		t.Fatalf("ingest key %q", key)
	}
	topics := e.pub.take("org")
	for _, want := range []string{"/api/environments/env", "/api/nodes/" + obsNodeAPI, "/api/nodes/" + obsNodeWorker} {
		if !obsContains(topics, want) {
			t.Errorf("topics %v lack %s", topics, want)
		}
	}

	e.exec(t, `UPDATE nodes SET dirty = 0 WHERE id = ?`, obsNodeAPI)
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true); err != nil {
		t.Fatal(err)
	}
	if n := e.node(t, obsNodeAPI); n.Dirty || obsOTLPKeyOf(t, e, "env") != key {
		t.Fatalf("re-enable: dirty=%v key changed=%v", n.Dirty, obsOTLPKeyOf(t, e, "env") != key)
	}
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeWorker, true); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT COUNT(*) FROM otlp_keys`) != 1 {
		t.Fatal("a second key was made")
	}
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, false); err != nil {
		t.Fatal(err)
	}
	if n := e.node(t, obsNodeAPI); n.Desired.Tracing || !n.Dirty {
		t.Fatalf("after disable: %+v dirty=%v", n.Desired, n.Dirty)
	}

	obsWantCode(t, e.app.SetNodeTracing(ctx, e.member, obsNodeDB, true), domain.CodeInvalidInput, "Only services can be traced")
	obsWantCode(t, e.app.SetNodeTracing(ctx, e.member, obsNodeVolume, false), domain.CodeInvalidInput, "Only services can be traced")
	obsWantCode(t, e.app.SetNodeTracing(ctx, e.member, "missing", true), domain.CodeServiceNotFound, "Node not found")
	obsWantCode(t, e.app.SetNodeTracing(ctx, e.signedOut, obsNodeAPI, true), domain.CodeNotAuthenticated, "Not authenticated")
}

func obsContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestNodeTracingView(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	e.app.Config.OTLPURL = ""
	e.app.Config.SiteURL = "http://100.64.0.1:8080"

	v, err := e.app.NodeTracing(ctx, e.member, obsNodeAPI)
	if err != nil {
		t.Fatal(err)
	}
	want := &domain.TracingView{Enabled: false, Traces: "off", Env: []domain.TracingEnvVar{
		{Key: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://100.64.0.1:8080/otlp"},
		{Key: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "http/protobuf"},
		{Key: "OTEL_EXPORTER_OTLP_HEADERS", Value: "Authorization=Bearer%20keel_otlp_…", Secret: true},
		{Key: "OTEL_SERVICE_NAME", Value: "api"},
		{Key: "OTEL_RESOURCE_ATTRIBUTES", Value: "keel.service_id=" + obsNodeAPI + ",keel.environment_id=env,deployment.environment.name=production"},
		{Key: "OTEL_TRACES_EXPORTER", Value: "otlp"},
		{Key: "OTEL_METRICS_EXPORTER", Value: "none"},
		{Key: "OTEL_LOGS_EXPORTER", Value: "none"},
	}}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("view\n got %+v\nwant %+v", v, want)
	}

	e.setSink(t, "org", obsTracesOn)
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true); err != nil {
		t.Fatal(err)
	}
	e.exec(t, `INSERT INTO variables (id, node_id, key, value, secret) VALUES ('v1', ?, 'OTEL_SERVICE_NAME', 'mine', 0), ('v2', ?, 'OTEL_EXPORTER_OTLP_TRACES_ENDPOINT', 'http://x', 0)`, obsNodeAPI, obsNodeAPI)
	e.app.Config.OTLPURL = "http://100.64.0.1:3211/otlp/"
	v, err = e.app.NodeTracing(ctx, e.member, obsNodeAPI)
	if err != nil {
		t.Fatal(err)
	}
	key := obsOTLPKeyOf(t, e, "env")
	if !v.Enabled || v.Traces != "on" {
		t.Fatalf("enabled=%v traces=%s", v.Enabled, v.Traces)
	}
	if v.Env[0].Value != "http://100.64.0.1:3211/otlp" {
		t.Errorf("endpoint %q", v.Env[0].Value)
	}
	if got := v.Env[2].Value; got != "Authorization=Bearer%20keel_otlp_…"+key[len(key)-4:] {
		t.Errorf("masked header %q", got)
	}
	over := map[string]bool{}
	for _, x := range v.Env {
		over[x.Key] = x.Overridden
	}
	if !over["OTEL_SERVICE_NAME"] || !over["OTEL_EXPORTER_OTLP_HEADERS"] || over["OTEL_EXPORTER_OTLP_ENDPOINT"] || over["OTEL_TRACES_EXPORTER"] {
		t.Errorf("overridden %v", over)
	}

	for _, c := range []struct {
		actor domain.Actor
		id    string
	}{{e.member, obsNodeDB}, {e.member, obsNodeVolume}, {e.foreigner, obsNodeAPI}, {e.member, obsNodeForeign}, {e.member, "missing"}, {e.signedOut, obsNodeAPI}} {
		if v, err := e.app.NodeTracing(ctx, c.actor, c.id); err != nil || v != nil {
			t.Errorf("NodeTracing(%+v, %s) = %+v, %v; want nil", c.actor, c.id, v, err)
		}
	}
}

func TestLocalTracingEnv(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	lt, err := e.app.LocalTracingEnv(ctx, e.member, obsNodeAPI)
	if err != nil || lt.Env != nil || lt.Reason != "Connect Axiom to see traces" {
		t.Fatalf("no sink: %+v %v", lt, err)
	}
	old := obsTracesOn
	old.Traces = ""
	e.setSink(t, "org", old)
	lt, _ = e.app.LocalTracingEnv(ctx, e.member, obsNodeAPI)
	if lt.Env != nil || lt.Reason != "Sign in with Axiom again to turn on traces" || obsOTLPKeyOf(t, e, "env") != "" {
		t.Fatalf("old sink: %+v", lt)
	}
	e.setSink(t, "org", obsTracesOn)
	lt, err = e.app.LocalTracingEnv(ctx, e.member, obsNodeAPI)
	if err != nil {
		t.Fatal(err)
	}
	key := obsOTLPKeyOf(t, e, "env")
	want := map[string]string{
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS":  "Authorization=Bearer%20" + key,
		"OTEL_SERVICE_NAME":           "api",
		"OTEL_RESOURCE_ATTRIBUTES":    "keel.service_id=" + obsNodeAPI + ",keel.environment_id=env,deployment.environment.name=local",
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_METRICS_EXPORTER":       "none",
		"OTEL_LOGS_EXPORTER":          "none",
	}
	if key == "" || lt.Reason != "" || !reflect.DeepEqual(lt.Env, want) {
		t.Fatalf("local env\n got %+v\nwant %+v", lt, want)
	}
	wantErr := func(actor domain.Actor, id, code, msg string) {
		t.Helper()
		_, err := e.app.LocalTracingEnv(ctx, actor, id)
		obsWantCode(t, err, code, msg)
	}
	wantErr(e.member, obsNodeDB, domain.CodeInvalidInput, "Only services can be traced")
	wantErr(e.foreigner, obsNodeAPI, domain.CodeServiceNotFound, "Node not found")
}

func TestTracingPromptNamesWhatTheCallerSees(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	prompt := func(actor domain.Actor, node, env string) string {
		t.Helper()
		p, err := e.app.TracingPrompt(ctx, actor, node, env)
		if err != nil {
			t.Fatal(err)
		}
		return strings.SplitN(p, "\n", 4)[2]
	}
	service := "Instrument this repo with OpenTelemetry so each request it serves becomes a trace in Keel, then prove it works locally before anything ships. It runs on Keel as the service `api` in the project `shop`."
	project := "Instrument this repo with OpenTelemetry so each request it serves becomes a trace in Keel, then prove it works locally before anything ships. It runs on Keel in the project `shop`; find which service it is with `keel service list` and use that name wherever this says `<service>`."
	none := "Instrument this repo with OpenTelemetry so each request it serves becomes a trace in Keel, then prove it works locally before anything ships. It runs on Keel; find which service it is with `keel service list` and use that name wherever this says `<service>`."
	cases := []struct {
		actor     domain.Actor
		node, env string
		want      string
	}{
		{e.member, obsNodeAPI, "", service},
		{e.member, obsNodeAPI, "env", service},
		{e.member, obsNodeDB, "env", project},
		{e.member, obsNodeDB, "", none},
		{e.member, "", "env", project},
		{e.member, "", "", none},
		{e.foreigner, obsNodeAPI, "env", none},
		{e.signedOut, obsNodeAPI, "env", none},
		{e.member, obsNodeForeign, "env2", none},
	}
	for _, c := range cases {
		if got := prompt(c.actor, c.node, c.env); got != c.want {
			t.Errorf("prompt(%s, %q, %q) line 3 = %q, want %q", c.actor.OrganizationID, c.node, c.env, got, c.want)
		}
	}
}
