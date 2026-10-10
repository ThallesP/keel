package app_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
)

func TestRelayTraces(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	ax := &obsFakeAxiom{forwardRes: app.HTTPReply{Status: 200, ContentType: "application/x-protobuf", Body: []byte{1, 2}}}
	e.app.Axiom = ax
	e.setSink(t, "org", obsTracesOn)
	if err := e.app.SetNodeTracing(ctx, e.member, obsNodeAPI, true); err != nil {
		t.Fatal(err)
	}
	key := obsOTLPKeyOf(t, e, "env")
	relay := func(key, ctype string, length int64, body string) app.HTTPReply {
		return e.app.RelayTraces(ctx, app.OTLPRequest{Key: key, ContentType: ctype, ContentLength: length, ContentEncoding: "gzip", Body: strings.NewReader(body)})
	}
	check := func(r app.HTTPReply, status int, ctype, body string) {
		t.Helper()
		if r.Status != status || r.ContentType != ctype || string(r.Body) != body {
			t.Fatalf("got %d %q %q, want %d %q %q", r.Status, r.ContentType, r.Body, status, ctype, body)
		}
	}

	check(relay("", "application/json", 2, "{}"), 401, "", "unauthorized")
	check(relay("keel_otlp_unknown", "application/json", 2, "{}"), 401, "", "unauthorized")
	check(relay(strings.TrimPrefix(key, "keel_otlp_"), "application/json", 2, "{}"), 401, "", "unauthorized")
	check(relay(key, "text/plain", 2, "{}"), 415, "", "OTLP over HTTP: application/x-protobuf or application/json")
	check(relay(key, "application/json", 4*1024*1024+1, "{}"), 413, "", "too large")
	check(relay(key, "application/json", -1, strings.Repeat("x", 4*1024*1024+1)), 413, "", "too large")
	if len(ax.forwarded) != 0 {
		t.Fatal("forwarded a rejected request")
	}

	body := "\x00\x01binary"
	check(relay(key, "Application/X-Protobuf; charset=x", int64(len(body)), body), 200, "application/x-protobuf", "\x01\x02")
	want := app.OTLPForward{Domain: "api.axiom.co", Token: obsTracesOn.Token, Dataset: "keel-traces", ContentType: "application/x-protobuf", ContentEncoding: "gzip", Body: []byte(body)}
	if len(ax.forwarded) != 1 || !reflect.DeepEqual(ax.forwarded[0], want) {
		t.Fatalf("forwarded %+v", ax.forwarded)
	}
	ax.forwardRes = app.HTTPReply{Status: 202, Body: []byte(`{"partialSuccess":{}}`)}
	check(relay(key, "application/json", -1, "{}"), 200, "application/json", `{"partialSuccess":{}}`)

	for _, c := range []struct {
		status   int
		body     string
		want     int
		wantBody string
	}{
		{429, "slow\n  down", 429, "slow down"},
		{502, "", 502, ""},
		{503, "x", 503, "x"},
		{504, "gw", 504, "gw"},
		{500, "", 503, "rejected"},
		{501, "nope", 503, "nope"},
		{400, "bad payload", 400, "bad payload"},
		{401, "", 400, "rejected"},
		{404, strings.Repeat("y", 300), 400, strings.Repeat("y", 200)},
	} {
		ax.forwardRes = app.HTTPReply{Status: c.status, Body: []byte(c.body)}
		check(relay(key, "application/json", -1, "{}"), c.want, "", c.wantBody)
	}
	ax.forwardErr = errors.New("dial tcp: refused")
	check(relay(key, "application/json", -1, "{}"), 503, "", "sink unreachable")
	ax.forwardErr = nil

	n := len(ax.forwarded)
	e.setSink(t, "org", obsTracesOld)
	check(relay(key, "application/json", -1, "{}"), 200, "application/json", "{}")
	check(relay(key, "application/x-protobuf", -1, "x"), 200, "application/x-protobuf", "")
	if err := e.app.DisconnectLogSink(ctx, e.member); err != nil {
		t.Fatal(err)
	}
	check(relay(key, "application/json", -1, "{}"), 200, "application/json", "{}")
	if len(ax.forwarded) != n {
		t.Fatal("forwarded without a traces dataset")
	}
	e.setSink(t, "org", obsTracesOn)
	ax.forwardRes = app.HTTPReply{Status: 401}
	check(relay(key, "application/json", -1, "{}"), 400, "", "rejected")

	e.exec(t, `DELETE FROM environments WHERE id = 'env'`)
	check(relay(key, "application/json", -1, "{}"), 401, "", "unauthorized")
}

func TestWorkerConfigPerSink(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_791_000_000_123)
	got, err := e.app.WorkerConfig(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("no sinks: %v %v", got, err)
	}
	e.exec(t, `INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('p3', 'org', 'Empty', 'empty', 3)`)
	e.exec(t, `INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('env3', 'p3', 'staging', 0, 3)`)
	e.exec(t, `INSERT INTO nodes (id, environment_id, type, name, desired_image, desired_revision, desired_replicas, created_at) VALUES ('stagingapiffffffffff', 'env3', 'service', 'api', 'nginx', 1, 1, 9)`)
	e.setSink(t, "org", obsTracesOnOrg)
	e.now++
	e.setSink(t, "org2", obsTracesOn)
	got, err = e.app.WorkerConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []app.WorkerSink{
		{ServiceIDs: []string{obsNodeAPI, obsNodeWorker, obsNodeDB, "stagingapiffffffffff"}, Sink: obsTracesOnOrg, Since: 1_791_000_000_123},
		{ServiceIDs: []string{obsNodeForeign}, Sink: obsTracesOn, Since: 1_791_000_000_124},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config\n got %+v\nwant %+v", got, want)
	}
}
