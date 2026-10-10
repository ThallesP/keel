package app

// One log sink per organization, shared by every project in it (logSinks.ts). Absent = the
// Docker default (read from the manager, ship nothing). The agent on every node polls
// /worker/config for the sink and the services of every project and streams container lines
// there; logs.go reads them back.

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

// LogSink is the caller's organization's sink as the UI may see it (kind and where, never the
// token); nil when signed out, without an organization, or without a sink.
func (a *App) LogSink(ctx context.Context, actor domain.Actor) (*domain.LogSinkView, error) {
	if !actor.SignedIn() || actor.OrganizationID == "" {
		return nil, nil
	}
	var view *domain.LogSinkView
	err := a.read(ctx, func(tx Tx) error {
		rec, err := orgSinkOf(tx, actor.OrganizationID)
		if err != nil || rec == nil {
			return err
		}
		s := rec.Sink
		view = &domain.LogSinkView{Kind: s.Kind, Domain: s.Domain, Dataset: s.Dataset, TokenHint: domain.TokenHint(s.Token)}
		if s.Traces != "" {
			view.Traces = &s.Traces
		}
		if s.Org != "" {
			view.Org = &s.Org
		}
		return nil
	})
	return view, err
}

// saveSink makes sink the actor's organization's, replacing whatever it had (a fresh row: its
// creation is the connect time the agent reads containers from).
func (a *App) saveSink(ctx context.Context, actor domain.Actor, sink domain.LogSink) error {
	if err := actor.RequireMember(); err != nil {
		return err
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		if err := tx.ReplaceLogSink(actor.OrganizationID, sink, a.Now()); err != nil {
			return err
		}
		sinkChanged(ch, actor.OrganizationID)
		return nil
	})
}

// DisconnectLogSink goes back to the Docker default for every project. The agents stop shipping
// on their next poll. Tracing switches and ingest keys stay: the relay then accepts and drops.
func (a *App) DisconnectLogSink(ctx context.Context, actor domain.Actor) error {
	if err := actor.RequireMember(); err != nil {
		return err
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		if err := tx.DeleteLogSink(actor.OrganizationID); err != nil {
			return err
		}
		sinkChanged(ch, actor.OrganizationID)
		return nil
	})
}

// ConnectAxiomInput is a pasted Axiom API token (no UI; scripts, agents, OAuth fallback).
type ConnectAxiomInput struct {
	Domain  string
	Dataset string
	Traces  *string // nil = no traces dataset
	Token   string
}

// ConnectAxiom verifies the token against Axiom (creating the datasets if needed), then makes it
// the organization's sink. Returns the logs and traces datasets.
func (a *App) ConnectAxiom(ctx context.Context, actor domain.Actor, in ConnectAxiomInput) (string, *string, error) {
	if err := actor.RequireMember(); err != nil {
		return "", nil, err
	}
	if !slices.Contains(domain.AxiomDomains, in.Domain) && !(a.Config.AllowLocalSinks && strings.Contains(in.Domain, "://")) {
		return "", nil, domain.Invalid(msgRegion)
	}
	names := []string{in.Dataset}
	if in.Traces != nil {
		names = append(names, *in.Traces)
	}
	for _, n := range names {
		if !domain.ValidDataset(n) {
			return "", nil, domain.Invalid(msgDataset)
		}
	}
	token := domain.TrimJS(in.Token)
	if len([]rune(token)) < 8 {
		return "", nil, domain.Invalid(msgNotAToken)
	}
	cfg := axiomCfg{Domain: in.Domain, Dataset: in.Dataset, Token: token}
	if err := a.axiomVerify(ctx, cfg); err != nil {
		return "", nil, obsInvalid(err)
	}
	sink := domain.LogSink{Kind: domain.SinkKindAxiom, Domain: in.Domain, Dataset: in.Dataset, Token: token}
	if in.Traces != nil {
		sink.Traces = *in.Traces
		if *in.Traces != "" {
			tcfg := cfg
			tcfg.Dataset = *in.Traces
			if err := a.axiomVerify(ctx, tcfg); err != nil {
				return "", nil, obsInvalid(err)
			}
		}
	}
	if err := a.saveSink(ctx, actor, sink); err != nil {
		return "", nil, err
	}
	return in.Dataset, in.Traces, nil
}

// BeginAxiomSignIn makes the PKCE verifier + state here (the browser may be on plain http, where
// WebCrypto is unavailable) and returns the authorize URL. Axiom redirects to /axiom/callback,
// which hands state + code to CompleteAxiomSignIn: exchange for a personal token, list orgs, and
// provision right away in the org picked on Axiom's consent page or the only one. Failing both,
// the token waits in axiom_pending until the Observability page calls ChooseAxiomOrg. Both
// pending tables are per organization, single use, and expire after 10 minutes.

// BeginAxiomSignIn returns Axiom's authorize URL for redirectURI (<origin>/axiom/callback).
func (a *App) BeginAxiomSignIn(ctx context.Context, actor domain.Actor, redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.EscapedPath() != "/axiom/callback" {
		return "", domain.Invalid(msgBadRedirect)
	}
	if err := actor.RequireMember(); err != nil {
		return "", err
	}
	var clientID string
	err = a.read(ctx, func(tx Tx) error {
		id, err := tx.AxiomClientFor(redirectURI)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		clientID = id
		return err
	})
	if err != nil {
		return "", err
	}
	if clientID == "" {
		id, err := a.Axiom.RegisterClient(ctx, a.axiomAuthURL(), redirectURI)
		if err != nil {
			var oe *OAuthError
			if errors.As(err, &oe) {
				return "", obsErr(domain.CodeInvalidInput, "Axiom refused to register Keel: "+oe.Error())
			}
			return "", obsInvalid(err)
		}
		// First writer wins; this sign-in still uses the client it registered.
		clientID = id
		err = a.write(ctx, func(tx Tx, _ *Changes) error { return tx.SaveAxiomClient(redirectURI, clientID, a.Now()) })
		if err != nil {
			return "", err
		}
	}
	state, verifier, authorize := a.axiomAuthorizeURL(clientID, redirectURI)
	err = a.write(ctx, func(tx Tx, _ *Changes) error {
		return tx.StartAxiomSignIn(AxiomSignIn{
			OrganizationID: actor.OrganizationID, ClientID: clientID, State: state, Verifier: verifier,
			RedirectURI: redirectURI, CreatedAt: a.Now(),
		})
	})
	if err != nil {
		return "", err
	}
	return authorize, nil
}

// AxiomSignInResult: Choose when the user has to pick an org first; else the provisioned sink.
type AxiomSignInResult struct {
	Choose  bool
	Dataset string
	Org     string
}

// CompleteAxiomSignIn is the /axiom/callback step.
func (a *App) CompleteAxiomSignIn(ctx context.Context, actor domain.Actor, state, code string) (AxiomSignInResult, error) {
	started, err := a.takeSignIn(ctx, actor, state)
	if err != nil {
		return AxiomSignInResult{}, err
	}
	if started == nil {
		return AxiomSignInResult{}, domain.Invalid(msgSignInExpired)
	}
	token, err := a.Axiom.ExchangeCode(ctx, a.axiomAuthURL(), AxiomCodeExchange{
		ClientID: started.ClientID, Code: code, Verifier: started.Verifier, RedirectURI: started.RedirectURI,
	})
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) {
			return AxiomSignInResult{}, obsErr(domain.CodeInvalidInput, "Axiom sign-in failed: "+oe.Error())
		}
		return AxiomSignInResult{}, obsInvalid(err)
	}
	orgs, err := a.axiomOrgs(ctx, token)
	if err != nil {
		return AxiomSignInResult{}, obsInvalid(err)
	}
	if len(orgs) == 0 {
		return AxiomSignInResult{}, domain.Invalid(msgNoAxiomOrg)
	}
	var org *domain.AxiomOrg
	if len(orgs) == 1 {
		org = &orgs[0]
	} else if chosen := axiomChosenOrg(token); chosen != "" {
		for i := range orgs {
			if orgs[i].ID == chosen {
				org = &orgs[i]
				break
			}
		}
	}
	if org != nil {
		dataset, name, err := a.provisionAxiom(ctx, actor, token, *org)
		if err != nil {
			return AxiomSignInResult{}, err
		}
		return AxiomSignInResult{Dataset: dataset, Org: name}, nil
	}
	if err := actor.RequireMember(); err != nil {
		return AxiomSignInResult{}, err
	}
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		if err := tx.StashAxiomPending(AxiomPending{OrganizationID: actor.OrganizationID, Token: token, Orgs: orgs, CreatedAt: a.Now()}); err != nil {
			return err
		}
		ch.Organization(actor.OrganizationID)
		return nil
	})
	if err != nil {
		return AxiomSignInResult{}, err
	}
	return AxiomSignInResult{Choose: true}, nil
}

// takeSignIn is the started sign-in for state, deleted on read. nil when unknown, expired, or
// another organization's (a foreign row is left alone).
func (a *App) takeSignIn(ctx context.Context, actor domain.Actor, state string) (*AxiomSignIn, error) {
	var out *AxiomSignIn
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		row, err := tx.AxiomSignInByState(state)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		if actor.OrganizationID == "" || row.OrganizationID != actor.OrganizationID {
			return nil
		}
		if err := tx.DeleteAxiomSignIn(state); err != nil {
			return err
		}
		if !a.axiomRowExpired(row.CreatedAt) {
			out = &row
		}
		return nil
	})
	return out, err
}

// ChooseAxiomOrg finishes a sign-in that saw several orgs. The pending pick is consumed even when
// orgID is not one of them (the user signs in again).
func (a *App) ChooseAxiomOrg(ctx context.Context, actor domain.Actor, orgID string) (string, string, error) {
	var pending *AxiomPending
	if actor.OrganizationID != "" {
		err := a.write(ctx, func(tx Tx, ch *Changes) error {
			p, err := tx.AxiomPendingOf(actor.OrganizationID)
			if errors.Is(err, ErrNoRow) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := tx.DeleteAxiomPending(actor.OrganizationID); err != nil {
				return err
			}
			ch.Organization(actor.OrganizationID)
			if !a.axiomRowExpired(p.CreatedAt) {
				pending = &p
			}
			return nil
		})
		if err != nil {
			return "", "", err
		}
	}
	if pending == nil {
		return "", "", domain.Invalid(msgPickExpired)
	}
	for _, o := range pending.Orgs {
		if o.ID == orgID {
			return a.provisionAxiom(ctx, actor, pending.Token, o)
		}
	}
	return "", "", domain.NotFound(msgOrgNotFound)
}

// PendingAxiomOrgs is the orgs to choose from while a sign-in with several orgs is pending (names
// only), else nil.
func (a *App) PendingAxiomOrgs(ctx context.Context, actor domain.Actor) ([]domain.AxiomOrgChoice, error) {
	if !actor.SignedIn() || actor.OrganizationID == "" {
		return nil, nil
	}
	var out []domain.AxiomOrgChoice
	err := a.read(ctx, func(tx Tx) error {
		p, err := tx.AxiomPendingOf(actor.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.axiomRowExpired(p.CreatedAt) {
			return nil
		}
		out = make([]domain.AxiomOrgChoice, len(p.Orgs))
		for i, o := range p.Orgs {
			out[i] = domain.AxiomOrgChoice{ID: o.ID, Name: o.Name}
		}
		return nil
	})
	return out, err
}

// CancelAxiomSignIn drops a pending org pick (the user backed out). No-op without one.
func (a *App) CancelAxiomSignIn(ctx context.Context, actor domain.Actor) error {
	if !actor.SignedIn() || actor.OrganizationID == "" {
		return nil
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		_, err := tx.AxiomPendingOf(actor.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := tx.DeleteAxiomPending(actor.OrganizationID); err != nil {
			return err
		}
		ch.Organization(actor.OrganizationID)
		return nil
	})
}

// provisionAxiom creates the datasets and the scoped token with the personal token, proves the
// token can query both, and saves the sink. The personal token is never stored; the token being
// replaced stays valid in Axiom.
func (a *App) provisionAxiom(ctx context.Context, actor domain.Actor, token string, org domain.AxiomOrg) (string, string, error) {
	if err := actor.RequireMember(); err != nil {
		return "", "", err
	}
	var slug string
	err := a.read(ctx, func(tx Tx) error {
		s, err := tx.SinkOrganizationSlug(actor.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return domain.ErrNoOrganization
		}
		slug = s
		return err
	})
	if err != nil {
		return "", "", err
	}
	sink, err := a.axiomProvision(ctx, token, org, "keel-"+slug)
	if err != nil {
		return "", "", obsInvalid(err)
	}
	for _, dataset := range []string{sink.Dataset, sink.Traces} {
		if err := a.axiomCanQuery(ctx, axiomCfg{Domain: sink.Domain, Dataset: dataset, Token: sink.Token}); err != nil {
			return "", "", obsErr(domain.CodeInvalidInput, "Querying "+dataset+": "+err.Error())
		}
	}
	sink.Org = org.Name
	if err := a.saveSink(ctx, actor, sink); err != nil {
		return "", "", err
	}
	return sink.Dataset, org.Name, nil
}

// WorkerSinkEntry is one project's routing for the agents (GET /worker/config).
type WorkerSinkEntry struct {
	ProjectID  string
	ServiceIDs []string
	Sink       domain.LogSink
	Since      int64 // when the organization connected the sink
}

// WorkerConfig is what every node's agent receives: per project with an organization sink, its
// services (nodes with desired set) and the sink (token included). Bearer-protected by the
// transport; no actor.
func (a *App) WorkerConfig(ctx context.Context) ([]WorkerSinkEntry, error) {
	out := []WorkerSinkEntry{}
	err := a.read(ctx, func(tx Tx) error {
		hasSink, err := tx.AnyLogSink()
		if err != nil || !hasSink {
			return err
		}
		projects, err := tx.WorkerSinkProjects()
		if err != nil {
			return err
		}
		sinks := map[string]*SinkRecord{}
		for _, p := range projects {
			if p.OrganizationID == "" {
				continue
			}
			rec, seen := sinks[p.OrganizationID]
			if !seen {
				if rec, err = orgSinkOf(tx, p.OrganizationID); err != nil {
					return err
				}
				sinks[p.OrganizationID] = rec
			}
			if rec == nil {
				continue
			}
			out = append(out, WorkerSinkEntry{ProjectID: p.ProjectID, ServiceIDs: p.ServiceIDs, Sink: rec.Sink, Since: rec.ConnectedAt})
		}
		return nil
	})
	return out, err
}
