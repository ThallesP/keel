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

	w.restart(app.Config{})
	delete(w.swarm.pullErr, "big:1")
	w.swarm.onPull = nil
	w.app.Recover(ctx)
	w.jobs.advance(time.Second)
	if d := w.deployment(id); d.Status != domain.DeploymentSuccess || len(w.swarm.creates) != 2 {
		t.Fatalf("after restart: %s %s creates %d", d.Status, stepStatuses(d), len(w.swarm.creates))
	}
}

func TestRecoverDoesNotApplyTwice(t *testing.T) {
	w := newWorld(t)
	w.addNode("api")
	id := w.ship(app.ShipOptions{})
	w.app.Recover(ctx)
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

func TestDockerCallsHaveDeadlines(t *testing.T) {
	w := newWorld(t)
	a := w.addNode("api")
	w.ship(app.ShipOptions{})
	w.jobs.advance(time.Second)
	w.app.IngestWorkerEvents(ctx, []app.DockerEvent{{Type: "node", Action: "update"}}, true)
	w.jobs.run()
	w.deleteNode(a.ID)
	w.app.ScheduleRemoveService(a.ID)
	w.app.Recover(ctx)
	w.jobs.advance(time.Second)
	if len(w.swarm.creates) != 1 || len(w.swarm.removed) != 1 {
		t.Fatalf("calls missing: creates %d removed %d", len(w.swarm.creates), len(w.swarm.removed))
	}
	if len(w.swarm.undated) != 0 {
		t.Fatalf("Docker calls without a deadline: %v", w.swarm.undated)
	}
}

func TestDeploymentLogKeepsLast500(t *testing.T) {
	w := newWorld(t)
	w.addNode("api")
	id := w.ship(app.ShipOptions{})
	for batch := range 2 {
		var lines []domain.LogLine
		for i := range 300 {
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
