package api

import "github.com/ThallesP/keel/internal/domain"

type (
	LogTail       = domain.LogTail
	TraceOverview = domain.TraceOverview
	TracingEnvVar = domain.TracingEnvVar
)

type LogSinkEnvelope struct {
	Sink *domain.LogSinkView `json:"sink" doc:"null when the organization has no sink (Docker default)"`
}

type ConnectAxiomRequest struct {
	Domain  string  `json:"domain" doc:"api.axiom.co or api.eu.axiom.co"`
	Dataset string  `json:"dataset" doc:"Logs dataset"`
	Traces  *string `json:"traces,omitempty" doc:"Traces dataset"`
	Token   string  `json:"token" doc:"API token with ingest and query on the datasets"`
}

type ConnectAxiomResult struct {
	Dataset string  `json:"dataset"`
	Traces  *string `json:"traces" nullable:"true"`
}

type PendingAxiomOrgs struct {
	Orgs []domain.AxiomOrgChoice `json:"orgs" nullable:"true" doc:"null when no sign-in waits for an org pick"`
}

type BeginAxiomSignInRequest struct {
	RedirectURI string `json:"redirectUri" doc:"<dashboard origin>/axiom/callback"`
}

type BeginAxiomSignInResult struct {
	URL string `json:"url"`
}

type CompleteAxiomSignInRequest struct {
	State string `json:"state"`
	Code  string `json:"code"`
}

type CompleteAxiomSignInResult struct {
	Choose  bool   `json:"choose"`
	Dataset string `json:"dataset,omitempty"`
	Org     string `json:"org,omitempty"`
}

type ChooseAxiomOrgRequest struct {
	OrgID string `json:"orgId"`
}

type AxiomSinkResult struct {
	Dataset string `json:"dataset"`
	Org     string `json:"org"`
}

type TracingEnvelope struct {
	Tracing *domain.TracingView `json:"tracing" doc:"null when the node is not a service the caller can see"`
}

type SetTracingRequest struct {
	On bool `json:"on"`
}

type LocalTracingEnv struct {
	Env    map[string]string `json:"env" nullable:"true"`
	Reason *string           `json:"reason" nullable:"true"`
}

type TracingPrompt struct {
	Prompt string `json:"prompt"`
}
