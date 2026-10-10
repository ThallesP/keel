package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

func nodeWith(desiredRev, replicas int, o *domain.Observed) *domain.Node {
	return &domain.Node{ID: "n", Desired: &domain.Desired{Image: "nginx", Revision: desiredRev, Replicas: replicas}, Observed: o}
}

func TestSettleStep(t *testing.T) {
	const now = 50
	applied := domain.DeployStep{NodeID: "n", Label: "api", Status: domain.StepRunning, AppliedAt: i64(10)}
	failed := func(s domain.DeployStep) domain.DeployStep {
		s.Status, s.FinishedAt = domain.StepFailed, i64(now)
		return s
	}
	done := func(s domain.DeployStep) domain.DeployStep {
		s.Status, s.FinishedAt = domain.StepDone, i64(now)
		return s
	}
	notApplied := applied
	notApplied.AppliedAt = nil
	cases := []struct {
		name     string
		step     domain.DeployStep
		node     *domain.Node
		want     domain.DeployStep
		wantText string
	}{
		{"node deleted", applied, nil, failed(applied), "api: node deleted"},
		{"pending step of a deleted node", domain.DeployStep{NodeID: "n", Label: "api", Status: domain.StepPending}, nil,
			failed(domain.DeployStep{NodeID: "n", Label: "api", Status: domain.StepPending}), "api: node deleted"},
		{"not applied yet", notApplied, nodeWith(2, 1, &domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK}), notApplied, ""},
		{"not observed yet", applied, nodeWith(2, 1, nil), applied, ""},
		{"old revision still observed", applied, nodeWith(2, 1, &domain.Observed{Revision: 1, Running: 1, State: domain.ObservedOK}), applied, ""},
		{"crash loop", applied, nodeWith(2, 1, &domain.Observed{Revision: 2, State: domain.ObservedCrashloop, Error: "exit 1"}),
			failed(applied), "api: crash loop · exit 1"},
		{"crash loop without error", applied, nodeWith(2, 1, &domain.Observed{Revision: 2, State: domain.ObservedCrashloop}),
			failed(applied), "api: crash loop"},
		{"rolled back", applied, nodeWith(2, 1, &domain.Observed{Revision: 2, State: domain.ObservedFailed, Error: "update rolled back"}),
			failed(applied), "api: rolled back by Swarm · update rolled back"},
		{"healthy", applied, nodeWith(2, 2, &domain.Observed{Revision: 2, Running: 2, State: domain.ObservedOK}),
			done(applied), "api: 2/2 replicas running"},
		{"stopped", applied, nodeWith(3, 0, &domain.Observed{Revision: 3, Running: 0, State: domain.ObservedOK}),
			done(applied), "api: stopped"},
		{"ran to completion", applied, nodeWith(2, 1, &domain.Observed{Revision: 2, Completed: iptr(1), State: domain.ObservedCompleted}),
			done(applied), "api: ran to completion"},
		{"still rolling out", applied, nodeWith(2, 2, &domain.Observed{Revision: 2, Running: 1, State: domain.ObservedUpdating}), applied, ""},
	}
	for _, c := range cases {
		got, text := settleStep(c.step, c.node, now)
		if !reflect.DeepEqual(got, c.want) || text != c.wantText {
			t.Errorf("%s:\n got %+v %q\nwant %+v %q", c.name, got, text, c.want, c.wantText)
		}
	}
}

func TestSettleDeployment(t *testing.T) {
	const now = 99
	healthy := nodeWith(2, 1, &domain.Observed{Revision: 2, Running: 1, State: domain.ObservedOK})
	updating := nodeWith(2, 1, &domain.Observed{Revision: 2, Running: 0, State: domain.ObservedUpdating})
	crashed := nodeWith(2, 1, &domain.Observed{Revision: 2, State: domain.ObservedCrashloop})
	stopped := nodeWith(2, 0, &domain.Observed{Revision: 2, State: domain.ObservedOK})
	health := domain.DeployStep{Label: healthStepLabel, Status: domain.StepPending}
	step := func(id string, status domain.StepStatus, applied bool) domain.DeployStep {
		s := domain.DeployStep{NodeID: id, Label: id, Status: status}
		if applied {
			s.AppliedAt = i64(5)
		}
		return s
	}
	dep := func(msg string, steps ...domain.DeployStep) domain.Deployment {
		return domain.Deployment{ID: "d", Message: msg, Status: domain.DeploymentRunning, StartedAt: 1, Steps: steps}
	}

	type want struct {
		status  domain.DeploymentStatus
		health  domain.StepStatus
		texts   []string
		changed bool
	}
	cases := []struct {
		name  string
		d     domain.Deployment
		nodes map[string]*domain.Node
		want  want
	}{
		{"pending steps wait for apply", dep("ship a", step("a", domain.StepPending, false), health),
			map[string]*domain.Node{"a": healthy}, want{domain.DeploymentRunning, domain.StepPending, nil, false}},
		{"applied but not converged: health starts", dep("ship a", step("a", domain.StepRunning, true), health),
			map[string]*domain.Node{"a": updating}, want{domain.DeploymentRunning, domain.StepRunning, nil, true}},
		{"some still pending: health waits", dep("ship a, b", step("a", domain.StepRunning, true), step("b", domain.StepPending, false), health),
			map[string]*domain.Node{"a": updating, "b": healthy}, want{domain.DeploymentRunning, domain.StepPending, nil, false}},
		{"all healthy", dep("ship a", step("a", domain.StepRunning, true), health),
			map[string]*domain.Node{"a": healthy}, want{domain.DeploymentSuccess, domain.StepDone, []string{"a: 1/1 replicas running", "all replicas healthy"}, true}},
		{"a stop says stopped", dep("stop a", step("a", domain.StepRunning, true), health),
			map[string]*domain.Node{"a": stopped}, want{domain.DeploymentSuccess, domain.StepDone, []string{"a: stopped", "stopped"}, true}},
		{"one crashes", dep("ship a, b", step("a", domain.StepRunning, true), step("b", domain.StepRunning, true), health),
			map[string]*domain.Node{"a": crashed, "b": updating}, want{domain.DeploymentFailed, domain.StepFailed, []string{"a: crash loop"}, true}},
		{"a node deleted while pending", dep("ship a", step("a", domain.StepPending, false), health),
			map[string]*domain.Node{"a": nil}, want{domain.DeploymentFailed, domain.StepFailed, []string{"a: node deleted"}, true}},
	}
	for _, c := range cases {
		before := append([]domain.DeployStep(nil), c.d.Steps...)
		next, appended, changed := settleDeployment(c.d, c.nodes, now)
		if !reflect.DeepEqual(before, c.d.Steps) {
			t.Errorf("%s: input mutated", c.name)
		}
		var texts []string
		for _, l := range appended {
			texts = append(texts, l.Text)
			if l.At != now {
				t.Errorf("%s: line at %d", c.name, l.At)
			}
		}
		h := next.Steps[len(next.Steps)-1]
		if next.Status != c.want.status || h.Status != c.want.health || !reflect.DeepEqual(texts, c.want.texts) || changed != c.want.changed {
			t.Errorf("%s: got status=%s health=%s texts=%q changed=%v", c.name, next.Status, h.Status, texts, changed)
		}
		if (next.Status == domain.DeploymentRunning) != (next.FinishedAt == nil) {
			t.Errorf("%s: finishedAt %v with status %s", c.name, next.FinishedAt, next.Status)
		}
	}
	d := dep("ship a", step("a", domain.StepRunning, true), domain.DeployStep{Label: healthStepLabel, Status: domain.StepRunning, StartedAt: i64(7)})
	next, _, _ := settleDeployment(d, map[string]*domain.Node{"a": healthy}, now)
	if h := next.Steps[1]; *h.StartedAt != 7 || *h.FinishedAt != now {
		t.Errorf("health: %+v", h)
	}
}

func TestDeployErrorText(t *testing.T) {
	if got := deployErrorText(errors.New("  Error response\n\tfrom daemon:  no such image \u00a0")); got != "Error response from daemon: no such image" {
		t.Errorf("collapse: %q", got)
	}
	long := strings.Repeat("é", 299) + "😀😀"
	if got := deployErrorText(errors.New(long)); got != strings.Repeat("é", 299)+"😀" {
		t.Errorf("cut: %d", len(got))
	}
	if got := deployErrorText(errors.New(strings.Repeat("x", 400))); len(got) != 300 {
		t.Errorf("cut ascii: %d", len(got))
	}
}

func TestDeployToFixed1(t *testing.T) {
	for in, want := range map[float64]string{
		0:      "0.0",
		3.4:    "3.4",
		1.25:   "1.3",
		0.25:   "0.3",
		1.15:   "1.1",
		1.05:   "1.1",
		9.96:   "10.0",
		224.04: "224.0",
		0.04:   "0.0",
	} {
		if got := deployToFixed1(in); got != want {
			t.Errorf("toFixed1(%v) = %q, want %q", in, got, want)
		}
	}
}
