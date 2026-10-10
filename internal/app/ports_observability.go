package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

type ObservabilityTx interface {
	LogSinkOf(organizationID string) (domain.LogSink, error)
	ReplaceLogSink(organizationID string, sink domain.LogSink, connectedAt int64) error
	DeleteLogSink(organizationID string) error
	WorkerSinks() ([]WorkerSink, error)

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
	OTLPKeyOrganization(key string) (string, error)
}

type WorkerSink struct {
	ServiceIDs []string       `json:"serviceIds"`
	Sink       domain.LogSink `json:"sink"`
	Since      int64          `json:"since"`
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

type AxiomRow map[string]json.RawMessage

type Axiom interface {
	Query(ctx context.Context, t AxiomTarget, q AxiomQuery) ([]AxiomRow, error)
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
	StartTime time.Time
	EndTime   time.Time
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
	ID          string
	Name        string
	Edge        string
	MaxDatasets int
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

func axiomStatus(err error) int {
	var axiomErr *AxiomError
	if !errors.As(err, &axiomErr) {
		return 0
	}
	return axiomErr.Status
}

type OAuthError struct {
	Status           int
	ErrorCode        string
	ErrorDescription string
}

func (e *OAuthError) Error() string {
	return cmp.Or(e.ErrorDescription, e.ErrorCode, "HTTP "+strconv.Itoa(e.Status))
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
