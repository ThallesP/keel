package sqlite

import (
	"encoding/json"
	"fmt"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	"github.com/ThallesP/keel/internal/gen/sqlc"
)

func (t *tx) LogSinkOf(organizationID string) (domain.LogSink, error) {
	r, err := t.q.ObsGetSink(t.ctx, organizationID)
	if err != nil {
		return domain.LogSink{}, noRow(err)
	}
	return sinkOf(r), nil
}

func sinkOf(r sqlc.LogSink) domain.LogSink {
	return domain.LogSink{Kind: r.Kind, Domain: r.Domain, Dataset: r.Dataset, Traces: str(r.Traces), Token: r.Token, Org: str(r.Org)}
}

func (t *tx) ReplaceLogSink(organizationID string, sink domain.LogSink, connectedAt int64) error {
	if err := t.q.ObsDeleteSink(t.ctx, organizationID); err != nil {
		return err
	}
	return t.q.ObsInsertSink(t.ctx, sqlc.ObsInsertSinkParams{
		ID:             domain.NewID(),
		OrganizationID: organizationID,
		Kind:           sink.Kind,
		Domain:         sink.Domain,
		Dataset:        sink.Dataset,
		Traces:         nullStr(sink.Traces),
		Token:          sink.Token,
		Org:            nullStr(sink.Org),
		CreatedAt:      connectedAt,
	})
}

func (t *tx) DeleteLogSink(organizationID string) error {
	return t.q.ObsDeleteSink(t.ctx, organizationID)
}

func (t *tx) WorkerSinks() ([]app.WorkerSink, error) {
	sinks, err := t.q.ObsListSinks(t.ctx)
	if err != nil {
		return nil, err
	}
	services, err := t.q.ObsListSinkServices(t.ctx)
	if err != nil {
		return nil, err
	}
	byOrganization := map[string][]string{}
	for _, s := range services {
		byOrganization[s.OrganizationID] = append(byOrganization[s.OrganizationID], s.ID)
	}
	out := make([]app.WorkerSink, len(sinks))
	for i, s := range sinks {
		out[i] = app.WorkerSink{ServiceIDs: byOrganization[s.OrganizationID], Sink: sinkOf(s), Since: s.CreatedAt}
	}
	return out, nil
}

func (t *tx) AxiomClientFor(redirectURI string) (string, error) {
	id, err := t.q.ObsGetAxiomClient(t.ctx, redirectURI)
	return id, noRow(err)
}

func (t *tx) SaveAxiomClient(redirectURI, clientID string, now int64) error {
	return t.q.ObsInsertAxiomClient(t.ctx, sqlc.ObsInsertAxiomClientParams{RedirectUri: redirectURI, ClientID: clientID, CreatedAt: now})
}

func (t *tx) StartAxiomSignIn(s app.AxiomSignIn) error {
	if err := t.q.ObsDeleteSignInsOfOrganization(t.ctx, s.OrganizationID); err != nil {
		return err
	}
	return t.q.ObsInsertSignIn(t.ctx, sqlc.ObsInsertSignInParams{
		State: s.State, OrganizationID: s.OrganizationID, ClientID: s.ClientID, Verifier: s.Verifier,
		RedirectUri: s.RedirectURI, CreatedAt: s.CreatedAt,
	})
}

func (t *tx) AxiomSignInByState(state string) (app.AxiomSignIn, error) {
	r, err := t.q.ObsGetSignIn(t.ctx, state)
	if err != nil {
		return app.AxiomSignIn{}, noRow(err)
	}
	return app.AxiomSignIn{
		OrganizationID: r.OrganizationID, ClientID: r.ClientID, State: r.State, Verifier: r.Verifier,
		RedirectURI: r.RedirectUri, CreatedAt: r.CreatedAt,
	}, nil
}

func (t *tx) DeleteAxiomSignIn(state string) error { return t.q.ObsDeleteSignIn(t.ctx, state) }

func (t *tx) PurgeAxiomSignIns(cutoff int64) error { return t.q.ObsPurgeSignIns(t.ctx, cutoff) }

func (t *tx) AxiomPendingOf(organizationID string) (app.AxiomPending, error) {
	r, err := t.q.ObsGetPending(t.ctx, organizationID)
	if err != nil {
		return app.AxiomPending{}, noRow(err)
	}
	var orgs []domain.AxiomOrg
	if err := json.Unmarshal([]byte(r.Orgs), &orgs); err != nil {
		return app.AxiomPending{}, fmt.Errorf("axiom_pending.orgs: %w", err)
	}
	return app.AxiomPending{OrganizationID: r.OrganizationID, Token: r.Token, Orgs: orgs, CreatedAt: r.CreatedAt}, nil
}

func (t *tx) StashAxiomPending(p app.AxiomPending) error {
	b, err := json.Marshal(p.Orgs)
	if err != nil {
		return err
	}
	return t.q.ObsUpsertPending(t.ctx, sqlc.ObsUpsertPendingParams{
		OrganizationID: p.OrganizationID, Token: p.Token, Orgs: string(b), CreatedAt: p.CreatedAt,
	})
}

func (t *tx) DeleteAxiomPending(organizationID string) error {
	return t.q.ObsDeletePending(t.ctx, organizationID)
}

func (t *tx) PurgeAxiomPending(cutoff int64) ([]string, error) {
	return t.q.ObsPurgePending(t.ctx, cutoff)
}

func (t *tx) OTLPKeyOf(environmentID string) (string, error) {
	k, err := t.q.ObsGetOTLPKey(t.ctx, environmentID)
	return k, noRow(err)
}

func (t *tx) InsertOTLPKey(environmentID, key string, now int64) error {
	return t.q.ObsInsertOTLPKey(t.ctx, sqlc.ObsInsertOTLPKeyParams{EnvironmentID: environmentID, Key: key, CreatedAt: now})
}

func (t *tx) OTLPKeyOrganization(key string) (string, error) {
	org, err := t.q.ObsGetOTLPKeyOrganization(t.ctx, key)
	return org, noRow(err)
}

func (t *tx) TracingVariableKeys(nodeID string) ([]string, error) {
	return t.q.ObsListVariableKeys(t.ctx, nodeID)
}
