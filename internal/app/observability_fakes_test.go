package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ThallesP/keel/internal/app"
)

// obsFakeAxiom is an in-memory app.Axiom. Errors are injected per method; every call is recorded
// as a short line ("Query keel-logs <apl>", "CreateDataset org=o1 keel-logs", …).
type obsFakeAxiom struct {
	mu    sync.Mutex
	calls []string

	queryErr    func(t app.AxiomTarget, q app.AxiomQuery) error
	queryRows   []*app.JSONObject
	createErr   map[string]error // by dataset name
	datasets    []app.AxiomDataset
	datasetsErr error
	minted      string
	mintErr     error
	mintReq     app.AxiomTokenRequest
	orgs        []app.AxiomOrgInfo
	orgsErr     error
	clientID    string
	registerErr error
	token       string
	exchangeErr error
	exchanged   app.AxiomCodeExchange
	forwarded   []app.OTLPForward
	forwardRes  app.HTTPReply
	forwardErr  error
}

func (f *obsFakeAxiom) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *obsFakeAxiom) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func (f *obsFakeAxiom) Query(_ context.Context, t app.AxiomTarget, q app.AxiomQuery) ([]*app.JSONObject, error) {
	f.record("Query %s %s %s", t.Domain, t.Token, q.APL)
	if f.queryErr != nil {
		if err := f.queryErr(t, q); err != nil {
			return nil, err
		}
	}
	return f.queryRows, nil
}

func (f *obsFakeAxiom) CreateDataset(_ context.Context, t app.AxiomTarget, orgID, name, description string) error {
	f.record("CreateDataset %s %s org=%s %s %q", t.Domain, t.Token, orgID, name, description)
	return f.createErr[name]
}

func (f *obsFakeAxiom) Datasets(_ context.Context, t app.AxiomTarget, orgID string) ([]app.AxiomDataset, error) {
	f.record("Datasets %s %s org=%s", t.Domain, t.Token, orgID)
	return f.datasets, f.datasetsErr
}

func (f *obsFakeAxiom) MintToken(_ context.Context, t app.AxiomTarget, orgID string, req app.AxiomTokenRequest) (string, error) {
	f.record("MintToken %s %s org=%s %s [%s]", t.Domain, t.Token, orgID, req.Name, strings.Join(req.Datasets, ","))
	f.mintReq = req
	return f.minted, f.mintErr
}

func (f *obsFakeAxiom) Orgs(_ context.Context, t app.AxiomTarget) ([]app.AxiomOrgInfo, error) {
	f.record("Orgs %s %s", t.Domain, t.Token)
	return f.orgs, f.orgsErr
}

func (f *obsFakeAxiom) RegisterClient(_ context.Context, authURL, redirectURI string) (string, error) {
	f.record("RegisterClient %s %s", authURL, redirectURI)
	return f.clientID, f.registerErr
}

func (f *obsFakeAxiom) ExchangeCode(_ context.Context, authURL string, x app.AxiomCodeExchange) (string, error) {
	f.record("ExchangeCode %s %s", authURL, x.Code)
	f.exchanged = x
	return f.token, f.exchangeErr
}

func (f *obsFakeAxiom) ForwardTraces(_ context.Context, fw app.OTLPForward) (app.HTTPReply, error) {
	f.mu.Lock()
	f.forwarded = append(f.forwarded, fw)
	f.mu.Unlock()
	return f.forwardRes, f.forwardErr
}

// obsFakeLogReader is an in-memory app.LogReader.
type obsFakeLogReader struct {
	body     []byte
	found    bool
	err      error
	tasks    []app.LogReplica
	tasksErr error
	service  string
	tail     int
}

func (r *obsFakeLogReader) ReadServiceLogs(_ context.Context, service string, tail int) ([]byte, bool, error) {
	r.service, r.tail = service, tail
	return r.body, r.found, r.err
}

func (r *obsFakeLogReader) ListLogReplicas(context.Context, string) ([]app.LogReplica, error) {
	return r.tasks, r.tasksErr
}
