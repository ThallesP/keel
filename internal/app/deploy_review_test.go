package app_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// TestApplyInterruptedByShutdown: serve stopping cancels the jobs' context mid-pull. That is not
// the apply's failure: the deployment stays running with the step unapplied (no "error:" line,
// no applyError), the rest of the node's queue is dropped, and the next start's recovery pass
// applies it.
func TestApplyInterruptedByShutdown(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api", image("big:1"))
	b := w.addNode("worker", image("big:1"))
	stopping, stop := context.WithCancel(context.Background())
	w.jobs.ctx = stopping
	w.swarm.pullErr["big:1"] = context.Canceled
	w.swarm.onPull = func(string) { stop() }
	id := w.ship(app.ShipOptions{})
	w.jobs.run()

	d := w.deployment(id)
	if d.Status != domain.DeploymentRunning || d.FinishedAt != nil {
		t.Fatalf("shutdown failed the deployment: %s %s %q", d.Status, stepStatuses(d), logTexts(d))
	}
	for i, s := range d.Steps[:2] {
		if s.Status == domain.StepFailed || s.AppliedAt != nil {
			t.Fatalf("step %d: %+v", i, s)
		}
	}
	for _, line := range logTexts(d) {
		if strings.HasPrefix(line, "error:") {
			t.Fatalf("log: %q", logTexts(d))
		}
	}
	for _, n := range []domain.Node{w.node(a.ID), w.node(b.ID)} {
		if n.ApplyError != "" {
			t.Fatalf("node %s: applyError %q", n.Name, n.ApplyError)
		}
	}

	// Next start: the recovery pass applies both.
	w.restart(app.Config{})
	delete(w.swarm.pullErr, "big:1")
	w.swarm.onPull = nil
	w.app.Recover(ctx)
	w.jobs.advance(time.Second)
	if d := w.deployment(id); d.Status != domain.DeploymentSuccess || len(w.swarm.creates) != 2 {
		t.Fatalf("after restart: %s %s creates %d", d.Status, stepStatuses(d), len(w.swarm.creates))
	}
}

// TestRecoverDoesNotApplyTwice: serve answers requests while its recovery pass runs, so the pass
// can read a deployment shipped a moment earlier whose applies are still queued. Re-queuing them
// must not reach Swarm twice for the same deployment and node.
func TestRecoverDoesNotApplyTwice(t *testing.T) {
	w := newWorld(t)
	w.addNode("api")
	id := w.ship(app.ShipOptions{}) // its apply is queued, not run yet
	w.app.Recover(ctx)              // reads the deployment as running with a pending step
	w.jobs.advance(time.Second)
	d := w.deployment(id)
	if len(w.swarm.creates) != 1 || len(w.swarm.updates) != 0 {
		t.Fatalf("creates %d updates %d: %q", len(w.swarm.creates), len(w.swarm.updates), logTexts(d))
	}
	applied := 0
	for _, line := range logTexts(d) {
		if strings.HasPrefix(line, "service ") {
			applied++
		}
	}
	if applied != 1 || d.Status != domain.DeploymentSuccess {
		t.Fatalf("log: %q (%s)", logTexts(d), d.Status)
	}
}

// TestDeploymentLogKeepsLast500: a deployment keeps its last 500 log lines (MAX_LOG), oldest
// first.
func TestDeploymentLogKeepsLast500(t *testing.T) {
	w := newWorld(t)
	w.addNode("api")
	id := w.ship(app.ShipOptions{})
	for batch := 0; batch < 2; batch++ {
		var lines []domain.LogLine
		for i := 0; i < 300; i++ {
			lines = append(lines, domain.LogLine{At: int64(batch*300 + i), Text: "line " + strconv.Itoa(batch*300+i)})
		}
		err := w.store.Write(ctx, func(tx app.Tx) error {
			d, err := tx.Deployment(id)
			if err != nil {
				return err
			}
			return tx.UpdateDeployment(d, lines)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := logTexts(w.deployment(id))
	if len(got) != 500 || got[0] != "line 100" || got[499] != "line 599" {
		t.Fatalf("%d lines, first %q last %q", len(got), got[0], got[len(got)-1])
	}
}
