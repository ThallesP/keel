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
		s, err := orgSinkOf(tx, actor.OrganizationID)
		if err != nil || s == nil {
			return err
		}
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

func (a *App) ConnectAxiom(ctx context.Context, actor domain.Actor, in ConnectAxiomInput) error {
	if err := actor.RequireMember(); err != nil {
		return err
	}
	if !slices.Contains(domain.AxiomDomains, in.Domain) && !(a.Config.AllowLocalSinks && strings.Contains(in.Domain, "://")) {
		return domain.Invalid("Region must be US or EU")
	}
	sink := domain.LogSink{Kind: domain.SinkKindAxiom, Domain: in.Domain, Dataset: in.Dataset, Token: strings.TrimSpace(in.Token)}
	datasets := []string{in.Dataset}
	if in.Traces != nil {
		sink.Traces = *in.Traces
		datasets = append(datasets, sink.Traces)
	}
	if slices.ContainsFunc(datasets, func(d string) bool { return !domain.ValidDataset(d) }) {
		return domain.Invalid("Dataset: letters, digits, - _ . only")
	}
	if len(sink.Token) < 8 {
		return domain.Invalid("That does not look like an Axiom API token")
	}
	for _, d := range datasets {
		if err := a.axiomVerify(ctx, sink, d); err != nil {
			return obsInvalid(err)
		}
	}
	return a.saveSink(ctx, actor, sink)
}

func (a *App) BeginAxiomSignIn(ctx context.Context, actor domain.Actor, redirectURI string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.EscapedPath() != "/axiom/callback" {
		return "", domain.Invalid("Bad redirect URI")
	}
	if err := actor.RequireMember(); err != nil {
		return "", err
	}
	var clientID string
	err = a.read(ctx, func(tx Tx) (err error) {
		clientID, err = tx.AxiomClientFor(redirectURI)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		return err
	})
	if err != nil {
		return "", err
	}
	if clientID == "" {
		id, err := a.Axiom.RegisterClient(ctx, a.axiomAuthURL(), redirectURI)
		var oe *OAuthError
		if errors.As(err, &oe) {
			return "", domain.Invalid("Axiom refused to register Keel: %s", oe)
		}
		if err != nil {
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
	if err := actor.RequireMember(); err != nil {
		return AxiomSignInResult{}, err
	}
	started, err := a.takeSignIn(ctx, actor, state)
	if err != nil {
		return AxiomSignInResult{}, err
	}
	if started == nil {
		return AxiomSignInResult{}, domain.Invalid("Axiom sign-in expired, try again")
	}
	token, err := a.Axiom.ExchangeCode(ctx, a.axiomAuthURL(), AxiomCodeExchange{
		ClientID: started.ClientID, Code: code, Verifier: started.Verifier, RedirectURI: started.RedirectURI,
	})
	var oe *OAuthError
	if errors.As(err, &oe) {
		return AxiomSignInResult{}, domain.Invalid("Axiom sign-in failed: %s", oe)
	}
	if err != nil {
		return AxiomSignInResult{}, obsInvalid(err)
	}
	orgs, err := a.axiomOrgs(ctx, token)
	if err != nil {
		return AxiomSignInResult{}, obsInvalid(err)
	}
	if len(orgs) == 0 {
		return AxiomSignInResult{}, domain.Invalid("This Axiom account has no organization")
	}
	chosen := axiomClaims(token).DefaultOrg
	if i := slices.IndexFunc(orgs, func(o domain.AxiomOrg) bool { return len(orgs) == 1 || o.ID == chosen }); i >= 0 {
		return a.provisionAxiom(ctx, actor, token, orgs[i])
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
		if row.OrganizationID != actor.OrganizationID {
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

func (a *App) ChooseAxiomOrg(ctx context.Context, actor domain.Actor, orgID string) (AxiomSignInResult, error) {
	if err := actor.RequireMember(); err != nil {
		return AxiomSignInResult{}, err
	}
	var pending *AxiomPending
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
		return AxiomSignInResult{}, err
	}
	if pending == nil {
		return AxiomSignInResult{}, domain.Invalid("Sign-in expired, sign in with Axiom again")
	}
	i := slices.IndexFunc(pending.Orgs, func(o domain.AxiomOrg) bool { return o.ID == orgID })
	if i < 0 {
		return AxiomSignInResult{}, domain.NotFound("Organization not found")
	}
	return a.provisionAxiom(ctx, actor, pending.Token, pending.Orgs[i])
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
		if err := tx.DeleteAxiomPending(actor.OrganizationID); err != nil {
			return err
		}
		ch.Organization(actor.OrganizationID)
		return nil
	})
}

func (a *App) provisionAxiom(ctx context.Context, actor domain.Actor, token string, org domain.AxiomOrg) (AxiomSignInResult, error) {
	var organization domain.Organization
	err := a.read(ctx, func(tx Tx) (err error) {
		organization, err = tx.AuthOrganization(actor.OrganizationID)
		return err
	})
	if err != nil {
		return AxiomSignInResult{}, err
	}
	sink, err := a.axiomProvision(ctx, token, org, "keel-"+organization.Slug)
	if err != nil {
		return AxiomSignInResult{}, obsInvalid(err)
	}
	for _, dataset := range []string{sink.Dataset, sink.Traces} {
		if err := a.axiomCanQuery(ctx, sink, dataset); err != nil {
			return AxiomSignInResult{}, domain.Invalid("Querying %s: %s", dataset, err)
		}
	}
	if err := a.saveSink(ctx, actor, sink); err != nil {
		return AxiomSignInResult{}, err
	}
	return AxiomSignInResult{Dataset: sink.Dataset, Org: sink.Org}, nil
}

func (a *App) WorkerConfig(ctx context.Context) ([]WorkerSink, error) {
	var sinks []WorkerSink
	err := a.read(ctx, func(tx Tx) (err error) {
		sinks, err = tx.WorkerSinks()
		return err
	})
	return sinks, err
}
