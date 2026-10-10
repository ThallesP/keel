package sqlite

import (
	"encoding/json"
	"fmt"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
	"github.com/ThallesP/keel/internal/gen/sqlc"
)

func (t *tx) LogSinkOf(organizationID string) (app.SinkRecord, error) {
	r, err := t.q.ObsGetSink(t.ctx, organizationID)
	if err != nil {
		return app.SinkRecord{}, noRow(err)
	}
	return app.SinkRecord{
		OrganizationID: r.OrganizationID,
		Sink: domain.LogSink{
			Kind: r.Kind, Domain: r.Domain, Dataset: r.Dataset, Traces: str(r.Traces), Token: r.Token, Org: str(r.Org),
		},
		ConnectedAt: r.CreatedAt,
	}, nil
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

func (t *tx) AnyLogSink() (bool, error) {
	n, err := t.q.ObsCountSinks(t.ctx)
	return n > 0, err
}

func (t *tx) WorkerSinkProjects() ([]app.WorkerProject, error) {
	projects, err := t.q.ObsListProjects(t.ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := t.q.ObsListDesiredNodes(t.ctx)
	if err != nil {
		return nil, err
	}
	byProject := map[string][]string{}
	for _, n := range nodes {
		byProject[n.ProjectID] = append(byProject[n.ProjectID], n.ID)
	}
	out := make([]app.WorkerProject, 0, len(projects))
	for _, p := range projects {
		ids := byProject[p.ID]
		if ids == nil {
			ids = []string{}
		}
		out = append(out, app.WorkerProject{ProjectID: p.ID, OrganizationID: p.OrganizationID, ServiceIDs: ids})
	}
	return out, nil
}

func (t *tx) SinkOrganizationSlug(organizationID string) (string, error) {
	s, err := t.q.ObsGetOrganizationSlug(t.ctx, organizationID)
	return s, noRow(err)
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
	orgs := p.Orgs
	if orgs == nil {
		orgs = []domain.AxiomOrg{}
	}
	b, err := json.Marshal(orgs)
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

func (t *tx) OTLPKeyEnvironment(key string) (string, error) {
	env, err := t.q.ObsGetOTLPKeyEnvironment(t.ctx, key)
	return env, noRow(err)
}

func (t *tx) TracingVariableKeys(nodeID string) ([]string, error) {
	return t.q.ObsListVariableKeys(t.ctx, nodeID)
}
