package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func obsFrame(stream byte, text string) []byte {
	n := len(text)
	return append([]byte{stream, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}, text...)
}

func TestTailNodeLogsFromDocker(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	logs := &obsFakeLogReader{
		found: true,
		body:  append(obsFrame(1, "2026-10-08T12:00:01.000000001Z com.docker.swarm.task.id=t2 b\n"), obsFrame(2, "2026-10-08T12:00:00Z com.docker.swarm.task.id=t1 a\n")...),
		tasks: []app.SwarmTask{{ID: "t2", Slot: 2, State: "running"}, {ID: "t1", Slot: 1, State: "shutdown"}, {ID: "t0", Slot: 1}},
	}
	e.app.Logs = logs
	tail, err := e.app.TailNodeLogs(ctx, e.member, obsNodeAPI, 5000)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.LogTail{Source: "docker",
		Lines: []domain.ServiceLogLine{
			{Time: 1791460800000, Text: "a", Stream: "stderr", Task: "t1"},
			{Time: 1791460801000, Text: "b", Stream: "stdout", Task: "t2"},
		},
		Replicas: []domain.LogReplica{{Task: "t0", Slot: 1, State: "unknown"}, {Task: "t1", Slot: 1, State: "shutdown"}, {Task: "t2", Slot: 2, State: "running"}},
	}
	if !reflect.DeepEqual(tail, want) || logs.service != "svc-"+obsNodeAPI || logs.tail != 1000 {
		t.Fatalf("tail %+v (service %s, tail %d)", tail, logs.service, logs.tail)
	}

	// Task listing failures only drop the replicas; a missing service is an empty tail.
	logs.tasksErr = errors.New("boom")
	tail, _ = e.app.TailNodeLogs(ctx, e.member, obsNodeAPI, 200)
	if len(tail.Replicas) != 0 || len(tail.Lines) != 2 {
		t.Fatalf("tasks error: %+v", tail)
	}
	logs.found, logs.body = false, nil
	tail, err = e.app.TailNodeLogs(ctx, e.member, obsNodeVolume, 200)
	if err != nil || tail.Source != "docker" || tail.Lines == nil || tail.Replicas == nil || len(tail.Lines)+len(tail.Replicas) != 0 {
		t.Fatalf("missing service: %+v %v", tail, err)
	}
	// Docker failures are server errors (not wrapped), as before.
	logs.err = errors.New("socket gone")
	if _, err := e.app.TailNodeLogs(ctx, e.member, obsNodeAPI, 200); err == nil || domain.CodeOf(err) != domain.CodeServerError {
		t.Fatalf("docker error: %v", err)
	}
}

// A member of another organization can neither read nor change anything of this one: every id
// of it reads as missing.
func TestObservabilityIsScopedToTheOrganization(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	e.app.Axiom = &obsFakeAxiom{}
	e.app.Logs = &obsFakeLogReader{found: true}
	e.setSink(t, "org", obsTracesOn)
	f := e.foreigner

	_, err := e.app.TailNodeLogs(ctx, f, obsNodeAPI, 100)
	obsWantCode(t, err, domain.CodeServiceNotFound, "Node not found")
	_, err = e.app.EnvironmentLogs(ctx, f, "env", "", 300, "")
	obsWantCode(t, err, domain.CodeProjectNotFound, "Environment not found")
	_, err = e.app.LogsAround(ctx, f, "env", 1)
	obsWantCode(t, err, domain.CodeProjectNotFound, "Environment not found")
	_, err = e.app.TraceOverview(ctx, f, "env", domain.Range1h, "", "")
	obsWantCode(t, err, domain.CodeProjectNotFound, "Environment not found")
	_, err = e.app.GetTrace(ctx, f, "env", "4bf92f3577b34da6a3ce929d0e0e4736", 0)
	obsWantCode(t, err, domain.CodeProjectNotFound, "Environment not found")
	_, err = e.app.TracesAround(ctx, f, "env", 1)
	obsWantCode(t, err, domain.CodeProjectNotFound, "Environment not found")
	obsWantCode(t, e.app.SetNodeTracing(ctx, f, obsNodeAPI, true), domain.CodeServiceNotFound, "Node not found")
	if v, _ := e.app.NodeTracing(ctx, f, obsNodeAPI); v != nil {
		t.Fatal("foreign tracing view")
	}
	if n := e.node(t, obsNodeAPI); n.Desired.Tracing || n.Dirty {
		t.Fatal("a foreign member changed the node")
	}
	// The foreign organization has no sink of its own: its own environment says so.
	_, err = e.app.TraceOverview(ctx, f, "env2", domain.Range1h, "", "")
	obsWantCode(t, err, domain.CodeTracesOff, "Connect Axiom to see traces")
	// Our own nodeId filter cannot reach into another environment.
	_, err = e.app.TraceOverview(ctx, e.member, "env", domain.Range1h, "", obsNodeForeign)
	obsWantCode(t, err, domain.CodeServiceNotFound, "Node not found")
	// Signed out.
	_, err = e.app.TailNodeLogs(ctx, e.signedOut, obsNodeAPI, 100)
	obsWantCode(t, err, domain.CodeNotAuthenticated, "Not authenticated")
	_, err = e.app.EnvironmentLogs(ctx, e.signedOut, "env", "", 300, "")
	obsWantCode(t, err, domain.CodeNotAuthenticated, "Not authenticated")
}

func TestLogsAndTracesNeedAStore(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000)
	e.app.Axiom = &obsFakeAxiom{}
	_, err := e.app.EnvironmentLogs(ctx, e.member, "env", "", 300, domain.Range1h)
	obsWantCode(t, err, domain.CodeInvalidInput, "Connect Axiom to search all logs")
	_, err = e.app.LogsAround(ctx, e.member, "env", 5)
	obsWantCode(t, err, domain.CodeInvalidInput, "Connect Axiom to search all logs")
	_, err = e.app.TracesAround(ctx, e.member, "env", 5)
	obsWantCode(t, err, domain.CodeTracesOff, "Connect Axiom to see traces")
	// "Not a trace id" is checked before anything else.
	_, err = e.app.GetTrace(ctx, e.signedOut, "missing", "xyz", 0)
	obsWantCode(t, err, domain.CodeInvalidInput, "Not a trace id")

	old := obsTracesOn
	old.Traces = ""
	e.setSink(t, "org", old)
	_, err = e.app.TraceOverview(ctx, e.member, "env", domain.Range15m, "", "")
	obsWantCode(t, err, domain.CodeTracesOff, "Sign in with Axiom again to turn on traces")
	around, err := e.app.TracesAround(ctx, e.member, "env", 5)
	if err != nil || around == nil || len(around) != 0 {
		t.Fatalf("around on an old sink: %v %v", around, err)
	}
	_, err = e.app.TraceOverview(ctx, e.member, "env", "2d", "", "")
	obsWantCode(t, err, domain.CodeInvalidInput, "Range must be one of 15m, 1h, 24h, 7d")
}

func TestEnvironmentWithoutServicesQueriesNothing(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_791_460_812_345)
	ax := &obsFakeAxiom{}
	e.app.Axiom = ax
	e.exec(t, `DELETE FROM nodes WHERE environment_id = 'env' AND type != 'volume'`)
	e.setSink(t, "org", obsTracesOn)
	o, err := e.app.TraceOverview(ctx, e.member, "env", domain.Range15m, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Buckets) != 30 || o.Stats.Requests != 0 || o.Stats.P50 != nil || len(o.Traces) != 0 || o.Traces == nil {
		t.Fatalf("overview %+v", o)
	}
	lines, err := e.app.EnvironmentLogs(ctx, e.member, "env", "x", 300, "")
	if err != nil || len(lines.Lines) != 0 || lines.Lines == nil {
		t.Fatalf("lines %+v %v", lines, err)
	}
	if calls := ax.take(); len(calls) != 0 {
		t.Fatalf("queried with no services: %q", calls)
	}
}
