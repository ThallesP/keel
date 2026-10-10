package app

import (
	"context"
	"strconv"

	"github.com/ThallesP/keel/internal/domain"
)

// ObservabilityTx: log sinks, Axiom sign-in state, OTLP keys.
// Owner: the observability area (docs/go/spec/observability.md).
type ObservabilityTx interface {
	// LogSinkOf is the organization's sink; ErrNoRow when it has none.
	LogSinkOf(organizationID string) (SinkRecord, error)
	// ReplaceLogSink deletes the organization's sink and inserts sink connected at connectedAt
	// (a fresh row: the connect time is where the agent starts reading containers).
	ReplaceLogSink(organizationID string, sink domain.LogSink, connectedAt int64) error
	DeleteLogSink(organizationID string) error
	// AnyLogSink: at least one organization has a sink.
	AnyLogSink() (bool, error)
	// WorkerSinkProjects is every project (creation order) with its organization and the ids of
	// its nodes that have `desired` set (creation order), for /worker/config.
	WorkerSinkProjects() ([]WorkerProject, error)
	// SinkOrganizationSlug is the organization's slug (the minted Axiom token is keel-<slug>).
	SinkOrganizationSlug(organizationID string) (string, error)

	// AxiomClientFor is the DCR client id registered for redirectURI; ErrNoRow when none.
	AxiomClientFor(redirectURI string) (string, error)
	// SaveAxiomClient inserts the client unless the URI already has one (first writer wins).
	SaveAxiomClient(redirectURI, clientID string, now int64) error
	// StartAxiomSignIn replaces the organization's in-flight sign-in with s.
	StartAxiomSignIn(s AxiomSignIn) error
	// AxiomSignInByState: ErrNoRow when unknown.
	AxiomSignInByState(state string) (AxiomSignIn, error)
	DeleteAxiomSignIn(state string) error
	// PurgeAxiomSignIns deletes sign-ins created at or before cutoff.
	PurgeAxiomSignIns(cutoff int64) error

	// AxiomPendingOf: ErrNoRow when the organization has no pending org pick.
	AxiomPendingOf(organizationID string) (AxiomPending, error)
	// StashAxiomPending replaces the organization's pending pick with p.
	StashAxiomPending(p AxiomPending) error
	DeleteAxiomPending(organizationID string) error
	// PurgeAxiomPending deletes picks created at or before cutoff and returns their organizations.
	PurgeAxiomPending(cutoff int64) ([]string, error)

	// OTLPKeyOf is the environment's ingest key; ErrNoRow when it has none yet.
	OTLPKeyOf(environmentID string) (string, error)
	// InsertOTLPKey stores the environment's key (the caller checked it has none).
	InsertOTLPKey(environmentID, key string, now int64) error
	// OTLPKeyEnvironment is the environment a key belongs to; ErrNoRow for an unknown key.
	OTLPKeyEnvironment(key string) (string, error)

	// TracingVariableKeys are the keys of the node's own variable rows.
	TracingVariableKeys(nodeID string) ([]string, error)
}

// SinkRecord is a log_sinks row.
type SinkRecord struct {
	OrganizationID string
	Sink           domain.LogSink
	ConnectedAt    int64 // the row's creation: the agent's start point for new containers
}

// WorkerProject is one project as /worker/config needs it.
type WorkerProject struct {
	ProjectID      string
	OrganizationID string
	ServiceIDs     []string
}

// AxiomSignIn is the PKCE state between authorize and callback (axiom_sign_ins).
type AxiomSignIn struct {
	OrganizationID string
	ClientID       string
	State          string
	Verifier       string
	RedirectURI    string
	CreatedAt      int64
}

// AxiomPending is a personal token waiting for an org pick (axiom_pending).
type AxiomPending struct {
	OrganizationID string
	Token          string
	Orgs           []domain.AxiomOrg
	CreatedAt      int64
}

// Axiom is Axiom's HTTP APIs: OAuth, datasets, queries, OTLP (adapters/axiom). Non-2xx answers
// of the REST calls are *AxiomError, refused OAuth calls *OAuthError; anything else is a network
// or decoding failure. Messages are built by the app from these.
type Axiom interface {
	// Query runs APL: POST <base>/v1/datasets/_apl?format=tabular → the first table's rows.
	Query(ctx context.Context, t AxiomTarget, q AxiomQuery) ([]*JSONObject, error)
	// CreateDataset: POST <base>/v2/datasets {name, description}; orgID "" sends no
	// x-axiom-org-id (API-token calls).
	CreateDataset(ctx context.Context, t AxiomTarget, orgID, name, description string) error
	// Datasets: GET <base>/v2/datasets with x-axiom-org-id.
	Datasets(ctx context.Context, t AxiomTarget, orgID string) ([]AxiomDataset, error)
	// MintToken: POST <base>/v2/tokens; returns the token ("" when Axiom returned none).
	MintToken(ctx context.Context, t AxiomTarget, orgID string, req AxiomTokenRequest) (string, error)
	// Orgs: GET <base>/v2/orgs with a personal token, no org header.
	Orgs(ctx context.Context, t AxiomTarget) ([]AxiomOrgInfo, error)
	// RegisterClient: DCR at <authURL>/oauth2/register; returns the client id.
	RegisterClient(ctx context.Context, authURL, redirectURI string) (string, error)
	// ExchangeCode: <authURL>/oauth2/token (authorization_code + PKCE); returns the access token.
	ExchangeCode(ctx context.Context, authURL string, x AxiomCodeExchange) (string, error)
	// ForwardTraces: POST <base>/v1/traces with x-axiom-dataset, the body unchanged. err is a
	// network failure or timeout; any HTTP answer is a reply.
	ForwardTraces(ctx context.Context, f OTLPForward) (HTTPReply, error)
}

// AxiomTarget is an Axiom host and the bearer token to use there.
type AxiomTarget struct {
	Domain string // api.axiom.co, or a full origin (see AxiomBaseURL)
	Token  string
}

// AxiomQuery is an APL query body. Times are ISO strings (JS toISOString).
type AxiomQuery struct {
	APL       string
	StartTime string
	EndTime   string
}

// AxiomDataset is an entry of GET /v2/datasets. Shared: sharedByOrg is set (Axiom's samples).
type AxiomDataset struct {
	Name   string
	Shared bool
}

// AxiomTokenRequest is the scoped API token Sign in with Axiom mints: ingest + query on Datasets.
type AxiomTokenRequest struct {
	Name        string
	Description string
	Datasets    []string
}

// AxiomOrgInfo is an org as GET /v2/orgs returns it (nil = absent).
type AxiomOrgInfo struct {
	ID                    string
	Name                  string
	DefaultEdgeDeployment *string
	Region                *string
	MaxDatasets           *float64 // license.maxDatasets
}

// AxiomCodeExchange is the token request of the authorization code grant.
type AxiomCodeExchange struct {
	ClientID    string
	Code        string
	Verifier    string
	RedirectURI string
}

// OTLPForward is one relayed OTLP/HTTP export.
type OTLPForward struct {
	Domain          string
	Token           string
	Dataset         string
	ContentType     string // normalized, without parameters
	ContentEncoding string // "" = none
	Body            []byte
}

// HTTPReply is an upstream HTTP answer.
type HTTPReply struct {
	Status      int
	ContentType string // "" when the upstream sent none
	Body        []byte
}

// AxiomError is a non-2xx answer of Axiom's REST API. Detail is the body, whitespace collapsed,
// trimmed, at most 200 UTF-16 units (see CompactDetail).
type AxiomError struct {
	Status int
	Detail string
}

// Error is "Axiom <status>" or "Axiom <status>: <detail>"; callers match on this text.
func (e *AxiomError) Error() string {
	if e.Detail == "" {
		return "Axiom " + strconv.Itoa(e.Status)
	}
	return "Axiom " + strconv.Itoa(e.Status) + ": " + e.Detail
}

// OAuthError is a refused DCR or token request: the HTTP status and the JSON body (nil when it
// was not a JSON object). Also returned on a 2xx without client_id / access_token.
type OAuthError struct {
	Status int
	Body   *JSONObject
}

// Error is the OAuth message: error_description || error || "HTTP <status>".
func (e *OAuthError) Error() string {
	for _, k := range []string{"error_description", "error"} {
		if v, ok := e.Body.Get(k); ok && jsTruthy(v) {
			return jsString(v)
		}
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

// LogReader reads container logs from Docker (the default sink: `docker service logs` on the
// manager). Implemented by adapters/swarm/logs.go.
type LogReader interface {
	// ReadServiceLogs is the raw body of GET /services/<service>/logs?stdout=1&stderr=1&tail=<n>
	// &timestamps=1&details=1 (multiplexed frames, or plain text for a TTY service). found=false
	// when the service does not exist (404).
	ReadServiceLogs(ctx context.Context, service string, tail int) (body []byte, found bool, err error)
	// ListLogReplicas is every task of the service (Swarm keeps history: exited ones included).
	ListLogReplicas(ctx context.Context, service string) ([]LogReplica, error)
}

// LogReplica is a Swarm task as the Logs tab tags lines with it. State "" = unknown.
type LogReplica struct {
	ID    string
	Slot  int
	State string
}
