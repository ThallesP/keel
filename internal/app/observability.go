package app

import (
	"context"
	"errors"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

const axiomPendingTTL = 10 * time.Minute

var (
	errNoSink     = domain.E(domain.CodeTracesOff, "Connect Axiom to see traces")
	errNoTraces   = domain.E(domain.CodeTracesOff, "Sign in with Axiom again to turn on traces")
	errNoLogStore = domain.Invalid("Connect Axiom to search all logs")
	errBadRange   = domain.Invalid("Range must be one of 15m, 1h, 24h, 7d")
)

func obsInvalid(err error) error {
	var de *domain.Error
	if errors.As(err, &de) || errors.Is(err, context.Canceled) {
		return err
	}
	return domain.Invalid("%s", err)
}

func orgSinkOf(tx Tx, org string) (*domain.LogSink, error) {
	sink, err := tx.LogSinkOf(org)
	if errors.Is(err, ErrNoRow) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sink, nil
}

type envSinkScope struct {
	Sink       domain.LogSink
	ServiceIDs []string
}

func (a *App) envSinkScope(ctx context.Context, actor domain.Actor, environmentID string, noSink error) (envSinkScope, error) {
	var s envSinkScope
	err := a.read(ctx, func(tx Tx) error {
		scope, err := requireEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		sink, err := orgSinkOf(tx, scope.Project.OrganizationID)
		if err != nil {
			return err
		}
		if sink == nil {
			return noSink
		}
		s.Sink = *sink
		nodes, err := tx.Nodes(environmentID)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			if n.Type != domain.NodeVolume && n.Type != domain.NodeGroup {
				s.ServiceIDs = append(s.ServiceIDs, n.ID)
			}
		}
		return nil
	})
	return s, err
}

func sinkChanged(ch *Changes, org string) {
	ch.Organization(org)
	ch.Add(org, "/api/nodes")
}

func (a *App) recoverObservability(ctx context.Context) {
	a.purgeAxiomState(ctx)
	a.Jobs.Every("observability.axiom-expiry", time.Minute, a.purgeAxiomState)
}

func (a *App) purgeAxiomState(ctx context.Context) {
	cutoff := a.Now() - axiomPendingTTL.Milliseconds()
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		if err := tx.PurgeAxiomSignIns(cutoff); err != nil {
			return err
		}
		orgs, err := tx.PurgeAxiomPending(cutoff)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			ch.Organization(org)
		}
		return nil
	})
	if err != nil {
		a.Log.Error("observability: purge expired Axiom sign-ins", "err", err)
	}
}

func (a *App) axiomRowExpired(createdAt int64) bool {
	return createdAt <= a.Now()-axiomPendingTTL.Milliseconds()
}
