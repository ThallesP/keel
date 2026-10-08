package app

import (
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

func rev(r string) map[string]string { return map[string]string{deployLabelRevision: r} }

func task(r, desired, state string, mods ...func(*SwarmTask)) SwarmTask {
	t := SwarmTask{DesiredState: desired, State: state, Labels: rev(r)}
	for _, m := range mods {
		m(&t)
	}
	return t
}

func onNode(id string) func(*SwarmTask) { return func(t *SwarmTask) { t.NodeID = id } }
func withErr(e string) func(*SwarmTask) { return func(t *SwarmTask) { t.Err = e } }
func at(ms int64) func(*SwarmTask)      { return func(t *SwarmTask) { t.Timestamp = ms } }
func noLabel() func(*SwarmTask)         { return func(t *SwarmTask) { t.Labels = map[string]string{} } }
func iptr(i int) *int                   { return &i }
func i64(i int64) *int64                { return &i }
func svc(r, update, msg string) *SwarmService {
	return &SwarmService{Name: "svc-x", Labels: rev(r), UpdateState: update, UpdateMessage: msg}
}

// TestSummarizeTasks pins swarm.ts summarize case by case.
func TestSummarizeTasks(t *testing.T) {
	const now = 1000
	cases := []struct {
		name  string
		tasks []SwarmTask
		svc   *SwarmService
		want  domain.Observed
	}{
		{"gone", nil, nil,
			domain.Observed{Revision: 0, Running: 0, State: domain.ObservedOK, NodeIDs: []string{}, At: now}},
		{"fresh create, running", []SwarmTask{task("1", "running", "running", onNode("n1"))}, svc("1", "", ""),
			domain.Observed{Revision: 1, Running: 1, State: domain.ObservedOK, NodeIDs: []string{"n1"}, At: now}},
		{"update in progress keeps updating", []SwarmTask{task("2", "running", "running", onNode("n1"))}, svc("2", "updating", ""),
			domain.Observed{Revision: 2, Running: 1, State: domain.ObservedUpdating, NodeIDs: []string{"n1"}, At: now}},
		{"update completed", []SwarmTask{task("2", "running", "running", onNode("n1"))}, svc("2", "completed", ""),
			domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK, NodeIDs: []string{"n1"}, At: now}},
		{"a live task not running yet", []SwarmTask{
			task("1", "running", "running", onNode("n1")),
			task("1", "running", "starting", onNode("n2")),
		}, svc("1", "", ""),
			domain.Observed{Revision: 1, Running: 1, State: domain.ObservedUpdating, NodeIDs: []string{"n1"}, At: now}},
		{"unschedulable", []SwarmTask{task("1", "running", "pending")}, svc("1", "", ""),
			domain.Observed{Revision: 1, Running: 0, State: domain.ObservedPending, NodeIDs: []string{}, At: now}},
		{"crash loop: five failed, last error wins", []SwarmTask{
			task("3", "shutdown", "failed", withErr("e1")),
			task("3", "shutdown", "rejected", withErr("e2")),
			task("3", "shutdown", "failed", withErr("e3")),
			task("3", "shutdown", "failed", withErr("e4")),
			task("3", "running", "failed", withErr("exit 1")),
		}, svc("3", "", ""),
			domain.Observed{Revision: 3, Running: 0, State: domain.ObservedCrashloop, NodeIDs: []string{}, Error: "exit 1", At: now}},
		{"four failed is not a crash loop (the live slot is not running: updating)", []SwarmTask{
			task("3", "shutdown", "failed"), task("3", "shutdown", "failed"),
			task("3", "shutdown", "failed"), task("3", "running", "failed", withErr("boom")),
		}, svc("3", "", ""),
			domain.Observed{Revision: 3, Running: 0, State: domain.ObservedUpdating, NodeIDs: []string{}, Error: "boom", At: now}},
		{"rolled back: revision names what failed", []SwarmTask{
			task("2", "running", "running", onNode("n1")),
			task("3", "shutdown", "failed", withErr("exit 1")),
		}, svc("2", "rollback_completed", "update rolled back due to failure"),
			domain.Observed{Revision: 3, Running: 0, State: domain.ObservedFailed, NodeIDs: []string{}, Error: "exit 1", At: now}},
		{"rolled back, failed task without error: the rollback message", []SwarmTask{
			task("3", "shutdown", "failed"),
		}, svc("2", "rollback_started", "rolling back"),
			domain.Observed{Revision: 3, Running: 0, State: domain.ObservedFailed, NodeIDs: []string{}, Error: "rolling back", At: now}},
		{"paused counts as rolled back", []SwarmTask{task("4", "running", "running", onNode("n1"))}, svc("4", "paused", "update paused"),
			domain.Observed{Revision: 4, Running: 1, State: domain.ObservedFailed, NodeIDs: []string{"n1"}, Error: "update paused", At: now}},
		{"one-shot: every task exited 0", []SwarmTask{
			task("5", "shutdown", "complete", at(300)),
			task("5", "shutdown", "complete", at(700)),
			task("4", "shutdown", "complete", at(900)),
		}, svc("5", "", ""),
			domain.Observed{Revision: 5, Running: 0, Completed: iptr(2), FinishedAt: i64(700), State: domain.ObservedCompleted, NodeIDs: []string{}, At: now}},
		{"one-shot with an unknown timestamp", []SwarmTask{task("1", "shutdown", "complete")}, nil,
			domain.Observed{Revision: 1, Running: 0, Completed: iptr(1), FinishedAt: i64(0), State: domain.ObservedCompleted, NodeIDs: []string{}, At: now}},
		{"completed beside a failure is not one-shot", []SwarmTask{
			task("1", "shutdown", "complete"), task("1", "shutdown", "failed"),
		}, nil,
			domain.Observed{Revision: 1, Running: 0, State: domain.ObservedOK, NodeIDs: []string{}, At: now}},
		{"old revisions are ignored", []SwarmTask{
			task("1", "shutdown", "failed", withErr("old")),
			task("2", "running", "running", onNode("n2")),
		}, svc("2", "", ""),
			domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK, NodeIDs: []string{"n2"}, At: now}},
		{"tasks without a revision label are not current", []SwarmTask{
			task("", "running", "running", noLabel(), onNode("n1")),
		}, svc("2", "", ""),
			domain.Observed{Revision: 2, Running: 0, State: domain.ObservedOK, NodeIDs: []string{}, At: now}},
		{"node ids distinct, first seen first, empty skipped", []SwarmTask{
			task("1", "running", "running", onNode("b")),
			task("1", "running", "running", onNode("a")),
			task("1", "running", "running", onNode("b")),
			task("1", "running", "running"),
		}, nil,
			domain.Observed{Revision: 1, Running: 4, State: domain.ObservedOK, NodeIDs: []string{"b", "a"}, At: now}},
		{"stopped service keeps its spec revision", nil, svc("7", "completed", ""),
			domain.Observed{Revision: 7, Running: 0, State: domain.ObservedOK, NodeIDs: []string{}, At: now}},
	}
	for _, c := range cases {
		got := summarizeTasks(c.tasks, c.svc, now)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

func TestTaskRevision(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   int
		ok     bool
	}{
		{nil, 0, false},
		{rev("3"), 3, true},
		{rev(" 4 "), 4, true},
		{rev(""), 0, true}, // Number("") is 0
		{rev("x"), 0, false},
		{rev("1.5"), 0, false},
	}
	for _, c := range cases {
		got, ok := taskRevision(c.labels)
		if got != c.want || ok != c.ok {
			t.Errorf("taskRevision(%v) = %d, %v; want %d, %v", c.labels, got, ok, c.want, c.ok)
		}
	}
}

func TestSettlingTasks(t *testing.T) {
	cases := []struct {
		name  string
		tasks []SwarmTask
		rev   int
		want  bool
	}{
		{"starting", []SwarmTask{task("2", "running", "starting")}, 2, true},
		{"preparing", []SwarmTask{task("2", "running", "preparing")}, 2, true},
		{"pending is the timeout's job", []SwarmTask{task("2", "running", "pending")}, 2, false},
		{"shut down", []SwarmTask{task("2", "shutdown", "starting")}, 2, false},
		{"another revision", []SwarmTask{task("1", "running", "starting")}, 2, false},
		{"running", []SwarmTask{task("2", "running", "running")}, 2, false},
		{"none", nil, 2, false},
	}
	for _, c := range cases {
		if got := settlingTasks(c.tasks, c.rev); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestObservedFace(t *testing.T) {
	n := domain.Node{Desired: &domain.Desired{Revision: 2, Replicas: 1}}
	n.Observed = &domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK, At: 1}
	a := observedFace(n)
	n.Observed = &domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK, At: 2}
	if observedFace(n) != a {
		t.Fatal("a new scan time alone must not count as a change")
	}
	n.Observed = &domain.Observed{Revision: 2, Running: 0, State: domain.ObservedUpdating, At: 3}
	if f := observedFace(n); f == a || f.step != "rolling out" || f.status != domain.StatusDeploying {
		t.Fatalf("updating: %+v", f)
	}
}
