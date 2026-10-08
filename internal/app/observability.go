package app

// Observability: shared pieces of the log sinks, logs, traces, OTLP relay and tracing switch
// (docs/go/spec/observability.md). Use cases live in sinks.go, logs.go, traces.go, tracing.go
// and otlp.go; the Axiom read side in axiom.go.

import (
	"context"
	"errors"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

// Messages (verbatim from the Convex functions; the CLI and the dashboard show them as is).
const (
	msgNoSink            = "Connect Axiom to see traces"
	msgNoTraces          = "Sign in with Axiom again to turn on traces"
	msgNoLogStore        = "Connect Axiom to search all logs"
	msgNotATraceID       = "Not a trace id"
	msgOnlyServicesTrace = "Only services can be traced"
	msgRegion            = "Region must be US or EU"
	msgDataset           = "Dataset: letters, digits, - _ . only"
	msgNotAToken         = "That does not look like an Axiom API token"
	msgBadRedirect       = "Bad redirect URI"
	msgSignInExpired     = "Axiom sign-in expired, try again"
	msgNoAxiomOrg        = "This Axiom account has no organization"
	msgPickExpired       = "Sign-in expired, sign in with Axiom again"
	msgOrgNotFound       = "Organization not found"
)

// axiomPendingTTL: how long a started sign-in or a pending org pick lives (logSinks PENDING_MS).
const axiomPendingTTL = 10 * time.Minute

// obsInvalid wraps a provider failure as the user-facing error (the TS wrapped them in
// ConvexError(err.message), which the CLI maps to INVALID_INPUT). Domain errors pass through.
func obsInvalid(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return err
	}
	return obsErr(domain.CodeInvalidInput, err.Error())
}

// obsErr is a domain error with a message used verbatim (never a format string).
func obsErr(code, msg string) error { return &domain.Error{Code: code, Message: msg} }

func errTracesOff(msg string) error { return obsErr(domain.CodeTracesOff, msg) }

func errNodeNotFound() error { return domain.E(domain.CodeServiceNotFound, domain.MsgNodeNotFound) }

// obsEnvironment is the environment when the actor may see it. Missing or foreign →
// "Environment not found" with PROJECT_NOT_FOUND, the code the CLI gives that message.
func obsEnvironment(tx Tx, actor domain.Actor, id string) (EnvScope, error) {
	if err := actor.RequireUser(); err != nil {
		return EnvScope{}, err
	}
	scope, ok, err := ownedEnvironment(tx, actor, id)
	if err != nil {
		return EnvScope{}, err
	}
	if !ok {
		return EnvScope{}, domain.E(domain.CodeProjectNotFound, domain.MsgEnvironmentNotFound)
	}
	return scope, nil
}

// sinkOf is the organization's sink, nil when it has none.
func sinkOf(tx Tx, org string) (*SinkRecord, error) {
	if org == "" {
		return nil, nil
	}
	r, err := tx.LogSinkOf(org)
	if errors.Is(err, ErrNoRow) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// envSinkScope is logSinks.forEnvironment: the environment's sink and the ids of every node that
// runs on Swarm (anything but volumes and groups, whatever its desired state).
type envSinkScope struct {
	EnvScope
	Sink       *domain.LogSink
	ServiceIDs []string
}

func (a *App) envSinkScope(ctx context.Context, actor domain.Actor, environmentID string) (envSinkScope, error) {
	var s envSinkScope
	err := a.read(ctx, func(tx Tx) error {
		scope, err := obsEnvironment(tx, actor, environmentID)
		if err != nil {
			return err
		}
		s.EnvScope = scope
		rec, err := sinkOf(tx, scope.Org)
		if err != nil {
			return err
		}
		if rec != nil {
			sink := rec.Sink
			s.Sink = &sink
		}
		nodes, err := tx.Nodes(environmentID)
		if err != nil {
			return err
		}
		s.ServiceIDs = []string{}
		for _, n := range nodes {
			if n.Type != domain.NodeVolume && n.Type != domain.NodeGroup {
				s.ServiceIDs = append(s.ServiceIDs, n.ID)
			}
		}
		return nil
	})
	return s, err
}

// sinkChanged: the organization's sink changed. Its Observability page and settings refetch, and
// so does every service's tracing view (it shows whether the organization can store traces).
func sinkChanged(ch *Changes, org string) {
	ch.Organization(org)
	ch.Add(org, "/api/nodes")
}

// recoverObservability purges started sign-ins and pending org picks older than 10 minutes, now
// and every minute after (they replace Convex's scheduled dropSignIn / dropPending). Reads also
// treat such rows as absent, so the expiry is exact even between sweeps.
func (a *App) recoverObservability(ctx context.Context) {
	a.purgeAxiomState(ctx)
	if a.Jobs != nil {
		a.Jobs.Every("observability.axiom-expiry", time.Minute, a.purgeAxiomState)
	}
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
			ch.Organization(org) // the org picker disappears
		}
		return nil
	})
	if err != nil {
		a.Log.Error("observability: purge expired Axiom sign-ins", "err", err)
	}
}

// expired: a sign-in or pending row created at createdAt is past its 10 minutes.
func (a *App) axiomRowExpired(createdAt int64) bool {
	return createdAt <= a.Now()-axiomPendingTTL.Milliseconds()
}
