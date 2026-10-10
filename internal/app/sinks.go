package app

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

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

type ConnectAxiomInput struct {
	Domain  string
	Dataset string
	Traces  *string
	Token   string
}

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
	token := strings.TrimSpace(in.Token)
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

type AxiomSignInResult struct {
	Choose  bool
	Dataset string
	Org     string
}

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

type WorkerSinkEntry struct {
	ProjectID  string
	ServiceIDs []string
	Sink       domain.LogSink
	Since      int64
}

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
