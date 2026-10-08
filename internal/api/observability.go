package api

import "github.com/ThallesP/keel/internal/domain"

// Observability wire types (docs/go/spec/observability.md, web-data.md §5.5, §5.7, §5.8). The
// read-model value types live in domain (one definition for server and CLI); these aliases are
// what the CLI imports. Times are epoch ms and durations ms, fractional.
type (
	ServiceLogLine     = domain.ServiceLogLine
	LogReplica         = domain.LogReplica
	LogTail            = domain.LogTail
	EnvironmentLogLine = domain.EnvironmentLogLine
	EnvironmentLogs    = domain.EnvironmentLogs
	Attribute          = domain.Attribute
	SpanEvent          = domain.SpanEvent
	Span               = domain.Span
	TraceSummary       = domain.TraceSummary
	TraceStats         = domain.TraceStats
	TraceBucket        = domain.TraceBucket
	TraceOverview      = domain.TraceOverview
	Trace              = domain.Trace
	TracingView        = domain.TracingView
	TracingEnvVar      = domain.TracingEnvVar
	LogSinkView        = domain.LogSinkView
	AxiomOrgChoice     = domain.AxiomOrgChoice
)

// LogSinkEnvelope is GET /api/organization/log-sink.
type LogSinkEnvelope struct {
	Sink *LogSinkView `json:"sink" doc:"null when the organization has no sink (Docker default)"`
}

// ConnectAxiomRequest pastes an Axiom API token (scripts, agents, OAuth fallback).
type ConnectAxiomRequest struct {
	Domain  string  `json:"domain" doc:"api.axiom.co or api.eu.axiom.co"`
	Dataset string  `json:"dataset" doc:"Logs dataset"`
	Traces  *string `json:"traces,omitempty" doc:"Traces dataset"`
	Token   string  `json:"token" doc:"API token with ingest and query on the datasets"`
}

// ConnectAxiomResult is what ConnectAxiom connected.
type ConnectAxiomResult struct {
	Dataset string  `json:"dataset"`
	Traces  *string `json:"traces" nullable:"true"`
}

// PendingAxiomOrgs is GET /api/organization/axiom/pending-orgs.
type PendingAxiomOrgs struct {
	Orgs []AxiomOrgChoice `json:"orgs" nullable:"true" doc:"null when no sign-in waits for an org pick"`
}

// BeginAxiomSignInRequest starts Sign in with Axiom.
type BeginAxiomSignInRequest struct {
	RedirectURI string `json:"redirectUri" doc:"<dashboard origin>/axiom/callback"`
}

// BeginAxiomSignInResult is the Axiom authorize URL to send the browser to.
type BeginAxiomSignInResult struct {
	URL string `json:"url"`
}

// CompleteAxiomSignInRequest is what Axiom redirected back with.
type CompleteAxiomSignInRequest struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

// CompleteAxiomSignInResult: choose=true when the user has to pick an org first; else the
// connected dataset and Axiom org.
type CompleteAxiomSignInResult struct {
	Choose  bool   `json:"choose"`
	Dataset string `json:"dataset,omitempty"`
	Org     string `json:"org,omitempty"`
}

// ChooseAxiomOrgRequest picks one of the pending orgs.
type ChooseAxiomOrgRequest struct {
	OrgID string `json:"orgId"`
}

// AxiomSinkResult is the dataset and Axiom org a sign-in connected.
type AxiomSinkResult struct {
	Dataset string `json:"dataset"`
	Org     string `json:"org"`
}

// TracingEnvelope is GET /api/nodes/{id}/tracing.
type TracingEnvelope struct {
	Tracing *TracingView `json:"tracing" doc:"null when the node is not a service the caller can see"`
}

// SetTracingRequest is PUT /api/nodes/{id}/tracing.
type SetTracingRequest struct {
	On bool `json:"on"`
}

// LocalTracingEnv is the tracing variables of a `keel run` (no endpoint: the CLI adds the
// address its machine reaches), or null with the reason when the organization cannot store
// traces.
type LocalTracingEnv struct {
	Env    map[string]string `json:"env" nullable:"true"`
	Reason *string           `json:"reason" nullable:"true"`
}

// TracingPrompt is the agent prompt (markdown).
type TracingPrompt struct {
	Prompt string `json:"prompt"`
}
