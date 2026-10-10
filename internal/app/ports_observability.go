package app

import (
	"context"
	"strconv"

	"github.com/ThallesP/keel/internal/domain"
)

type ObservabilityTx interface {
	LogSinkOf(organizationID string) (SinkRecord, error)
	ReplaceLogSink(organizationID string, sink domain.LogSink, connectedAt int64) error
	DeleteLogSink(organizationID string) error
	AnyLogSink() (bool, error)
	WorkerSinkProjects() ([]WorkerProject, error)
	SinkOrganizationSlug(organizationID string) (string, error)

	AxiomClientFor(redirectURI string) (string, error)
	SaveAxiomClient(redirectURI, clientID string, now int64) error
	StartAxiomSignIn(s AxiomSignIn) error
	AxiomSignInByState(state string) (AxiomSignIn, error)
	DeleteAxiomSignIn(state string) error
	PurgeAxiomSignIns(cutoff int64) error

	AxiomPendingOf(organizationID string) (AxiomPending, error)
	StashAxiomPending(p AxiomPending) error
	DeleteAxiomPending(organizationID string) error
	PurgeAxiomPending(cutoff int64) ([]string, error)

	OTLPKeyOf(environmentID string) (string, error)
	InsertOTLPKey(environmentID, key string, now int64) error
	OTLPKeyEnvironment(key string) (string, error)

	TracingVariableKeys(nodeID string) ([]string, error)
}

type SinkRecord struct {
	OrganizationID string
	Sink           domain.LogSink
	ConnectedAt    int64
}

type WorkerProject struct {
	ProjectID      string
	OrganizationID string
	ServiceIDs     []string
}

type AxiomSignIn struct {
	OrganizationID string
	ClientID       string
	State          string
	Verifier       string
	RedirectURI    string
	CreatedAt      int64
}

type AxiomPending struct {
	OrganizationID string
	Token          string
	Orgs           []domain.AxiomOrg
	CreatedAt      int64
}

type Axiom interface {
	Query(ctx context.Context, t AxiomTarget, q AxiomQuery) ([]*JSONObject, error)
	CreateDataset(ctx context.Context, t AxiomTarget, orgID, name, description string) error
	Datasets(ctx context.Context, t AxiomTarget, orgID string) ([]AxiomDataset, error)
	MintToken(ctx context.Context, t AxiomTarget, orgID string, req AxiomTokenRequest) (string, error)
	Orgs(ctx context.Context, t AxiomTarget) ([]AxiomOrgInfo, error)
	RegisterClient(ctx context.Context, authURL, redirectURI string) (string, error)
	ExchangeCode(ctx context.Context, authURL string, x AxiomCodeExchange) (string, error)
	ForwardTraces(ctx context.Context, f OTLPForward) (HTTPReply, error)
}

type AxiomTarget struct {
	Domain string
	Token  string
}

type AxiomQuery struct {
	APL       string
	StartTime string
	EndTime   string
}

type AxiomDataset struct {
	Name   string
	Shared bool
}

type AxiomTokenRequest struct {
	Name        string
	Description string
	Datasets    []string
}

type AxiomOrgInfo struct {
	ID                    string
	Name                  string
	DefaultEdgeDeployment *string
	Region                *string
	MaxDatasets           *float64
}

type AxiomCodeExchange struct {
	ClientID    string
	Code        string
	Verifier    string
	RedirectURI string
}

type OTLPForward struct {
	Domain          string
	Token           string
	Dataset         string
	ContentType     string
	ContentEncoding string
	Body            []byte
}

type HTTPReply struct {
	Status      int
	ContentType string
	Body        []byte
}

type AxiomError struct {
	Status int
	Detail string
}

func (e *AxiomError) Error() string {
	if e.Detail == "" {
		return "Axiom " + strconv.Itoa(e.Status)
	}
	return "Axiom " + strconv.Itoa(e.Status) + ": " + e.Detail
}

type OAuthError struct {
	Status int
	Body   *JSONObject
}

func (e *OAuthError) Error() string {
	for _, k := range []string{"error_description", "error"} {
		if v, ok := e.Body.Get(k); ok && jsTruthy(v) {
			return jsString(v)
		}
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

type LogReader interface {
	ReadServiceLogs(ctx context.Context, service string, tail int) (body []byte, found bool, err error)
	ListLogReplicas(ctx context.Context, service string) ([]LogReplica, error)
}

type LogReplica struct {
	ID    string
	Slot  int
	State string
}
