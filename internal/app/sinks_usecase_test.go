package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func obsSinkOfOrg(t *testing.T, e *obsEnv, org string) *domain.LogSink {
	t.Helper()
	var sink *domain.LogSink
	err := e.store.Read(context.Background(), func(tx app.Tx) error {
		s, err := tx.LogSinkOf(org)
		if errors.Is(err, app.ErrNoRow) {
			return nil
		}
		sink = &s
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return sink
}

func TestLogSinkViewAndDisconnect(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 5_000)
	if v, err := e.app.LogSink(ctx, e.member); err != nil || v != nil {
		t.Fatalf("no sink: %+v %v", v, err)
	}
	e.setSink(t, "org", domain.LogSink{Kind: "axiom", Domain: "api.eu.axiom.co", Dataset: "logs", Token: "xaat-secret-9z9z"})
	v, err := e.app.LogSink(ctx, e.member)
	if err != nil {
		t.Fatal(err)
	}
	want := &domain.LogSinkView{Kind: "axiom", Domain: "api.eu.axiom.co", Dataset: "logs", TokenHint: "…9z9z"}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("view %+v", v)
	}
	e.setSink(t, "org", obsTracesOnWithOrg())
	v, _ = e.app.LogSink(ctx, e.member)
	if v.Traces == nil || *v.Traces != "keel-traces" || v.Org == nil || *v.Org != "Acme Inc" {
		t.Fatalf("traces/org: %+v", v)
	}
	for _, a := range []domain.Actor{e.foreigner, e.signedOut, {UserID: "u3"}} {
		if v, err := e.app.LogSink(ctx, a); err != nil || v != nil {
			t.Errorf("LogSink(%+v) = %+v, %v", a, v, err)
		}
	}
	if err := e.app.DisconnectLogSink(ctx, e.foreigner); err != nil {
		t.Fatal(err)
	}
	if obsSinkOfOrg(t, e, "org") == nil {
		t.Fatal("a foreign disconnect removed the sink")
	}
	e.pub.take("org")
	if err := e.app.DisconnectLogSink(ctx, e.member); err != nil {
		t.Fatal(err)
	}
	if obsSinkOfOrg(t, e, "org") != nil {
		t.Fatal("still connected")
	}
	if got := e.pub.take("org"); !reflect.DeepEqual(got, []string{"/api/nodes", "/api/organization"}) {
		t.Errorf("topics %v", got)
	}
	obsWantCode(t, e.app.DisconnectLogSink(ctx, domain.Actor{UserID: "u3"}), domain.CodeNoOrganization, domain.MsgNoOrganization)
	obsWantCode(t, e.app.DisconnectLogSink(ctx, e.signedOut), domain.CodeNotAuthenticated, "Not authenticated")
}

func obsTracesOnWithOrg() domain.LogSink {
	s := obsTracesOn
	s.Org = "Acme Inc"
	return s
}

func TestConnectAxiom(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 7_000)
	ax := &obsFakeAxiom{}
	e.app.Axiom = ax
	connect := func(domainName, dataset string, traces *string, token string) error {
		return e.app.ConnectAxiom(ctx, e.member, app.ConnectAxiomInput{Domain: domainName, Dataset: dataset, Traces: traces, Token: token})
	}
	obsWantCode(t, connect("evil.example.com", "logs", nil, "xaat-12345678"), domain.CodeInvalidInput, "Region must be US or EU")
	obsWantCode(t, connect("http://127.0.0.1:4318", "logs", nil, "xaat-12345678"), domain.CodeInvalidInput, "Region must be US or EU")
	obsWantCode(t, connect("api.axiom.co", "-logs", nil, "xaat-12345678"), domain.CodeInvalidInput, "Dataset: letters, digits, - _ . only")
	obsWantCode(t, connect("api.axiom.co", "logs", new(""), "xaat-12345678"), domain.CodeInvalidInput, "Dataset: letters, digits, - _ . only")
	obsWantCode(t, connect("api.axiom.co", "logs", nil, "  short  "), domain.CodeInvalidInput, "That does not look like an Axiom API token")
	if len(ax.take()) != 0 {
		t.Fatal("Axiom was called before validation passed")
	}

	ax.createErr = map[string]error{"logs": &app.AxiomError{Status: 409, Detail: "dataset exists"}}
	if err := connect("api.axiom.co", "logs", new("spans"), "  xaat-12345678  "); err != nil {
		t.Fatal(err)
	}
	calls := ax.take()
	if len(calls) != 4 || !strings.HasPrefix(calls[0], `CreateDataset api.axiom.co xaat-12345678 org= logs "Keel container logs"`) ||
		!strings.HasPrefix(calls[1], "Query api.axiom.co xaat-12345678 ['logs'] | limit 1") ||
		!strings.HasPrefix(calls[2], `CreateDataset api.axiom.co xaat-12345678 org= spans "Keel container logs"`) ||
		!strings.HasPrefix(calls[3], "Query api.axiom.co xaat-12345678 ['spans'] | limit 1") {
		t.Fatalf("calls %q", calls)
	}
	sink := obsSinkOfOrg(t, e, "org")
	if sink == nil || *sink != (domain.LogSink{Kind: "axiom", Domain: "api.axiom.co", Dataset: "logs", Traces: "spans", Token: "xaat-12345678"}) ||
		e.count(t, `SELECT created_at FROM log_sinks WHERE organization_id = 'org'`) != 7_000 {
		t.Fatalf("saved %+v", sink)
	}

	ax.createErr = map[string]error{"logs": &app.AxiomError{Status: 403, Detail: "forbidden"}}
	obsWantCode(t, connect("api.axiom.co", "logs", nil, "xaat-12345678"), domain.CodeInvalidInput, "Axiom 403: forbidden")
	ax.createErr = map[string]error{"logs": &app.AxiomError{Status: 500}}
	ax.queryErr = func(app.AxiomTarget, app.AxiomQuery) error { return &app.AxiomError{Status: 401, Detail: "bad token"} }
	obsWantCode(t, connect("api.axiom.co", "logs", nil, "xaat-12345678"), domain.CodeInvalidInput, "Axiom 401: bad token")
	ax.queryErr = nil
	ax.createErr = nil

	e.app.Config.AllowLocalSinks = true
	if err := connect("http://127.0.0.1:4318", "logs", nil, "xaat-12345678"); err != nil {
		t.Fatal(err)
	}
	if sink := obsSinkOfOrg(t, e, "org"); sink.Domain != "http://127.0.0.1:4318" || sink.Traces != "" {
		t.Fatalf("local sink %+v", sink)
	}
	err := e.app.ConnectAxiom(ctx, domain.Actor{UserID: "u3"}, app.ConnectAxiomInput{Domain: "api.axiom.co", Dataset: "logs", Token: "xaat-12345678"})
	obsWantCode(t, err, domain.CodeNoOrganization, domain.MsgNoOrganization)
}

func TestBeginAxiomSignIn(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 10_000)
	ax := &obsFakeAxiom{clientID: "client-1"}
	e.app.Axiom = ax
	const redirect = "https://keel.example.ts.net/axiom/callback"

	for _, bad := range []string{"not a url", "ftp://x/axiom/callback", "https://x/axiom/callback/", "https://x/other", "/axiom/callback", "https://x"} {
		_, err := e.app.BeginAxiomSignIn(ctx, e.member, bad)
		obsWantCode(t, err, domain.CodeInvalidInput, "Bad redirect URI")
	}
	_, err := e.app.BeginAxiomSignIn(ctx, domain.Actor{UserID: "u3"}, redirect)
	obsWantCode(t, err, domain.CodeNoOrganization, domain.MsgNoOrganization)
	if len(ax.take()) != 0 {
		t.Fatal("DCR for a caller without an organization")
	}

	raw, err := e.app.BeginAxiomSignIn(ctx, e.member, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if calls := ax.take(); !reflect.DeepEqual(calls, []string{"RegisterClient https://authorization.axiom.co " + redirect}) {
		t.Fatalf("calls %q", calls)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != "https://authorization.axiom.co/oauth2/authorize" || q.Get("client_id") != "client-1" ||
		q.Get("response_type") != "code" || q.Get("redirect_uri") != redirect || q.Get("scope") != "openid profile email" ||
		q.Get("code_challenge_method") != "S256" || len(q.Get("state")) != 22 {
		t.Fatalf("authorize URL %s", raw)
	}
	if !strings.Contains(raw, "&redirect_uri=https%3A%2F%2Fkeel.example.ts.net%2Faxiom%2Fcallback&response_type=code&scope=openid+profile+email&state=") {
		t.Fatalf("parameter encoding: %s", raw)
	}
	var verifier, org string
	_ = e.store.DB().QueryRow(`SELECT verifier, organization_id FROM axiom_sign_ins WHERE state = ?`, q.Get("state")).Scan(&verifier, &org)
	sum := sha256.Sum256([]byte(verifier))
	if len(verifier) != 43 || org != "org" || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("PKCE: verifier %q org %q challenge %q", verifier, org, q.Get("code_challenge"))
	}

	e.app.Config.AllowLocalSinks, e.app.Config.AxiomAuthURL = true, "http://127.0.0.1:9999/"
	raw2, err := e.app.BeginAxiomSignIn(ctx, e.member, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if calls := ax.take(); len(calls) != 0 {
		t.Fatalf("registered again: %q", calls)
	}
	if !strings.HasPrefix(raw2, "http://127.0.0.1:9999/oauth2/authorize?client_id=client-1&") {
		t.Fatalf("override: %s", raw2)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins WHERE organization_id = 'org'`); n != 1 {
		t.Fatalf("%d sign-ins in flight", n)
	}

	ax.clientID, ax.registerErr = "", &app.OAuthError{Status: 400, ErrorCode: "invalid_redirect_uri"}
	_, err = e.app.BeginAxiomSignIn(ctx, e.member, "http://10.0.0.1/axiom/callback")
	obsWantCode(t, err, domain.CodeInvalidInput, "Axiom refused to register Keel: invalid_redirect_uri")
	ax.registerErr = &app.OAuthError{Status: 502}
	_, err = e.app.BeginAxiomSignIn(ctx, e.member, "http://10.0.0.2/axiom/callback")
	obsWantCode(t, err, domain.CodeInvalidInput, "Axiom refused to register Keel: HTTP 502")
}

func obsJWT(claims string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"
}

func obsStartSignIn(t *testing.T, e *obsEnv, actor domain.Actor) string {
	t.Helper()
	raw, err := e.app.BeginAxiomSignIn(context.Background(), actor, "https://keel.example.ts.net/axiom/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	return u.Query().Get("state")
}

func TestCompleteAxiomSignInSingleOrg(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 20_000)
	ax := &obsFakeAxiom{clientID: "client-1", token: obsJWT(`{"aud":"mcp"}`), minted: "xaat-minted-ABCD",
		orgs:     []app.AxiomOrgInfo{{ID: "acme-x1", Name: "Acme Axiom", Edge: "cloud.eu-central-1.aws", MaxDatasets: 3}},
		datasets: []app.AxiomDataset{{Name: "keel-logs"}, {Name: "otel-demo-traces", Shared: true}}}
	e.app.Axiom = ax
	state := obsStartSignIn(t, e, e.member)
	ax.take()

	_, err := e.app.CompleteAxiomSignIn(ctx, e.foreigner, state, "code")
	obsWantCode(t, err, domain.CodeInvalidInput, "Axiom sign-in expired, try again")
	if e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins WHERE state = ?`, state) != 1 {
		t.Fatal("a foreign callback consumed the sign-in")
	}

	e.pub.take("org")
	res, err := e.app.CompleteAxiomSignIn(ctx, e.member, state, "the-code")
	if err != nil {
		t.Fatal(err)
	}
	if res != (app.AxiomSignInResult{Dataset: "keel-logs", Org: "Acme Axiom"}) {
		t.Fatalf("result %+v", res)
	}
	want := []string{
		"ExchangeCode https://authorization.axiom.co the-code",
		"Orgs api.axiom.co " + ax.token,
		"Datasets api.eu.axiom.co " + ax.token + " org=acme-x1",
		"CreateDataset api.eu.axiom.co " + ax.token + ` org=acme-x1 keel-traces "Keel OpenTelemetry traces"`,
		"MintToken api.eu.axiom.co " + ax.token + " org=acme-x1 keel-acme [keel-logs,keel-traces]",
		"Query api.eu.axiom.co xaat-minted-ABCD ['keel-logs'] | limit 1",
		"Query api.eu.axiom.co xaat-minted-ABCD ['keel-traces'] | limit 1",
	}
	if got := ax.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls\n got %q\nwant %q", got, want)
	}
	if ax.exchanged.Verifier == "" || ax.exchanged.ClientID != "client-1" || ax.exchanged.RedirectURI != "https://keel.example.ts.net/axiom/callback" {
		t.Fatalf("exchange %+v", ax.exchanged)
	}
	if ax.mintReq.Description != "Keel: logs and traces go in, the control plane reads them back" {
		t.Fatalf("token description %q", ax.mintReq.Description)
	}
	if sink := obsSinkOfOrg(t, e, "org"); *sink != (domain.LogSink{Kind: "axiom", Domain: "api.eu.axiom.co", Dataset: "keel-logs", Traces: "keel-traces", Token: "xaat-minted-ABCD", Org: "Acme Axiom"}) {
		t.Fatalf("sink %+v", sink)
	}
	if got := e.pub.take("org"); !slices.Contains(got, "/api/organization") || !slices.Contains(got, "/api/nodes") {
		t.Errorf("topics %v", got)
	}
	if e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins`) != 0 {
		t.Fatal("the sign-in was not consumed")
	}
	_, err = e.app.CompleteAxiomSignIn(ctx, e.member, state, "the-code")
	obsWantCode(t, err, domain.CodeInvalidInput, "Axiom sign-in expired, try again")
}

func TestCompleteAxiomSignInFailures(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 30_000)
	ax := &obsFakeAxiom{clientID: "c", token: obsJWT(`{"aud":["a","b"]}`)}
	e.app.Axiom = ax
	complete := func() error {
		_, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code")
		return err
	}
	ax.exchangeErr = &app.OAuthError{Status: 400, ErrorCode: "invalid_grant", ErrorDescription: "code expired"}
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Axiom sign-in failed: code expired")
	ax.exchangeErr = nil
	ax.orgsErr = &app.AxiomError{Status: 401, Detail: "bad audience"}
	obsWantCode(t, complete(), domain.CodeInvalidInput, `Axiom 401: bad audience (Axiom API rejected the sign-in token, aud ["a","b"])`)
	ax.token = "opaque"
	obsWantCode(t, complete(), domain.CodeInvalidInput, `Axiom 401: bad audience (Axiom API rejected the sign-in token, aud (not a JWT))`)
	ax.token = obsJWT(`{}`)
	obsWantCode(t, complete(), domain.CodeInvalidInput, `Axiom 401: bad audience (Axiom API rejected the sign-in token, aud null)`)
	ax.orgsErr = nil
	obsWantCode(t, complete(), domain.CodeInvalidInput, "This Axiom account has no organization")

	ax.orgs = []app.AxiomOrgInfo{{ID: "o1", Name: "Free Org", MaxDatasets: 3}}
	ax.datasetsErr = &app.AxiomError{Status: 403}
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Listing datasets: Axiom 403")
	ax.datasetsErr = nil
	ax.datasets = []app.AxiomDataset{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "sample", Shared: true}}
	ax.createErr = map[string]error{"keel-logs": &app.AxiomError{Status: 400, Detail: "Bad Request"}}
	obsWantCode(t, complete(), domain.CodeInvalidInput,
		"Free Org is at its Axiom plan's limit of 3 datasets (a, b, c). Keel needs keel-logs and keel-traces: delete 2 in Axiom or pick another org. (Axiom 400: Bad Request)")
	ax.datasets = []app.AxiomDataset{{Name: "a"}, {Name: "b"}}
	ax.createErr = map[string]error{"keel-traces": &app.AxiomError{Status: 400, Detail: "Bad Request"}}
	obsWantCode(t, complete(), domain.CodeInvalidInput,
		"Free Org is at its Axiom plan's limit of 3 datasets (a, b, keel-logs). Keel needs keel-traces: delete 1 in Axiom or pick another org. (Axiom 400: Bad Request)")
	ax.datasets = nil
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Creating keel-traces: Axiom 400: Bad Request")
	ax.createErr = map[string]error{"keel-logs": &app.AxiomError{Status: 500, Detail: "oops"}}
	ax.datasets = []app.AxiomDataset{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Creating keel-logs: Axiom 500: oops")
	ax.createErr = nil
	ax.datasets = []app.AxiomDataset{{Name: "keel-logs"}, {Name: "keel-traces"}}
	ax.mintErr = &app.AxiomError{Status: 403, Detail: "no"}
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Minting the ingest token: Axiom 403: no")
	ax.mintErr = nil
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Axiom did not return a token")
	ax.minted = "xaat-1"
	ax.queryErr = func(_ app.AxiomTarget, q app.AxiomQuery) error {
		if strings.Contains(q.APL, "keel-traces") {
			return &app.AxiomError{Status: 403, Detail: "no read"}
		}
		return nil
	}
	obsWantCode(t, complete(), domain.CodeInvalidInput, "Querying keel-traces: Axiom 403: no read")
	if obsSinkOfOrg(t, e, "org") != nil {
		t.Fatal("a failed sign-in saved a sink")
	}

	ax.queryErr = nil
	state := obsStartSignIn(t, e, e.member)
	e.now += 10 * 60_000
	_, err := e.app.CompleteAxiomSignIn(ctx, e.member, state, "code")
	obsWantCode(t, err, domain.CodeInvalidInput, "Axiom sign-in expired, try again")
}

func TestAxiomOrgPick(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 40_000)
	orgs := []app.AxiomOrgInfo{
		{ID: "o1", Name: "One", Edge: "us-east-1"},
		{ID: "o2", Name: "Two", Edge: "cloud.us-east-1.aws"},
	}
	ax := &obsFakeAxiom{clientID: "c", token: obsJWT(`{"axiomDefaultOrg":"nope"}`), orgs: orgs, minted: "xaat-2"}
	e.app.Axiom = ax

	e.pub.take("org")
	res, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code")
	if err != nil || !res.Choose {
		t.Fatalf("pending: %+v %v", res, err)
	}
	if got := e.pub.take("org"); !slices.Contains(got, "/api/organization") {
		t.Errorf("topics %v", got)
	}
	choices, err := e.app.PendingAxiomOrgs(ctx, e.member)
	if err != nil || !reflect.DeepEqual(choices, []domain.AxiomOrgChoice{{ID: "o1", Name: "One"}, {ID: "o2", Name: "Two"}}) {
		t.Fatalf("pending orgs %+v %v", choices, err)
	}
	if c, _ := e.app.PendingAxiomOrgs(ctx, e.foreigner); c != nil {
		t.Fatalf("foreign sees the pick: %+v", c)
	}
	_, err = e.app.ChooseAxiomOrg(ctx, e.foreigner, "o1")
	obsWantCode(t, err, domain.CodeInvalidInput, "Sign-in expired, sign in with Axiom again")
	_, err = e.app.ChooseAxiomOrg(ctx, e.member, "o9")
	obsWantCode(t, err, domain.CodeNotFound, "Organization not found")
	_, err = e.app.ChooseAxiomOrg(ctx, e.member, "o1")
	obsWantCode(t, err, domain.CodeInvalidInput, "Sign-in expired, sign in with Axiom again")

	if _, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code"); err != nil {
		t.Fatal(err)
	}
	ax.take()
	res, err = e.app.ChooseAxiomOrg(ctx, e.member, "o2")
	if err != nil || res != (app.AxiomSignInResult{Dataset: "keel-logs", Org: "Two"}) {
		t.Fatalf("choose: %+v %v", res, err)
	}
	if sink := obsSinkOfOrg(t, e, "org"); sink.Domain != "api.axiom.co" || sink.Org != "Two" {
		t.Fatalf("sink %+v", sink)
	}
	if c, _ := e.app.PendingAxiomOrgs(ctx, e.member); c != nil {
		t.Fatal("pick not consumed")
	}

	ax.token = obsJWT(`{"axiomDefaultOrg":"o1"}`)
	res, err = e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code")
	if err != nil || res.Choose || res.Org != "One" {
		t.Fatalf("claimed org: %+v %v", res, err)
	}

	ax.token = obsJWT(`{}`)
	if _, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code"); err != nil {
		t.Fatal(err)
	}
	if err := e.app.CancelAxiomSignIn(ctx, e.foreigner); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.app.PendingAxiomOrgs(ctx, e.member); c == nil {
		t.Fatal("a foreign cancel dropped our pick")
	}
	if err := e.app.CancelAxiomSignIn(ctx, e.member); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.app.PendingAxiomOrgs(ctx, e.member); c != nil {
		t.Fatal("cancel kept the pick")
	}
	if _, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code"); err != nil {
		t.Fatal(err)
	}
	e.now += 10 * 60_000
	if c, _ := e.app.PendingAxiomOrgs(ctx, e.member); c != nil {
		t.Fatal("expired pick still listed")
	}
	_, err = e.app.ChooseAxiomOrg(ctx, e.member, "o1")
	obsWantCode(t, err, domain.CodeInvalidInput, "Sign-in expired, sign in with Axiom again")
}

func TestAxiomAPIOverride(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 50_000)
	ax := &obsFakeAxiom{clientID: "c", token: obsJWT(`{}`), orgs: []app.AxiomOrgInfo{{ID: "o1", Name: "Mock", Edge: "eu-1"}}, minted: "xaat-3"}
	e.app.Axiom = ax
	e.app.Config.AxiomAPIURL = "http://127.0.0.1:4318"
	if _, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code"); err != nil {
		t.Fatal(err)
	}
	if sink := obsSinkOfOrg(t, e, "org"); sink.Domain != "api.eu.axiom.co" {
		t.Fatalf("domain %s", sink.Domain)
	}
	e.app.Config.AllowLocalSinks = true
	ax.take()
	if _, err := e.app.CompleteAxiomSignIn(ctx, e.member, obsStartSignIn(t, e, e.member), "code"); err != nil {
		t.Fatal(err)
	}
	if sink := obsSinkOfOrg(t, e, "org"); sink.Domain != "http://127.0.0.1:4318" {
		t.Fatalf("domain %s", sink.Domain)
	}
	if calls := ax.take(); !strings.HasPrefix(calls[1], "Orgs http://127.0.0.1:4318 ") {
		t.Fatalf("calls %q", calls)
	}
}

func TestPurgeExpiredAxiomState(t *testing.T) {
	ctx := context.Background()
	e := newObsEnv(t, 1_000_000)
	e.exec(t, `INSERT INTO axiom_sign_ins (state, organization_id, client_id, verifier, redirect_uri, created_at) VALUES ('old', 'org', 'c', 'v', 'r', ?), ('new', 'org', 'c', 'v', 'r', ?)`, 1_000_000-600_000, 1_000_000-599_999)
	e.exec(t, `INSERT INTO axiom_pending (organization_id, token, orgs, created_at) VALUES ('org', 't', '[]', ?), ('org2', 't', '[]', ?)`, 1_000_000-600_000, 1_000_000)
	e.app.Recover(ctx)
	if e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins WHERE state = 'new'`) != 1 || e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins`) != 1 {
		t.Fatal("sign-ins not purged by age")
	}
	if e.count(t, `SELECT COUNT(*) FROM axiom_pending WHERE organization_id = 'org2'`) != 1 || e.count(t, `SELECT COUNT(*) FROM axiom_pending`) != 1 {
		t.Fatal("pending picks not purged by age")
	}
	if got := e.pub.take("org"); !reflect.DeepEqual(got, []string{"/api/organization"}) {
		t.Errorf("topics %v", got)
	}
	sweep := e.jobs.every["observability.axiom-expiry"]
	if sweep == nil {
		t.Fatal("no periodic sweep registered")
	}
	e.now += 600_000
	sweep(ctx)
	if e.count(t, `SELECT COUNT(*) FROM axiom_sign_ins`)+e.count(t, `SELECT COUNT(*) FROM axiom_pending`) != 0 {
		t.Fatal("the sweep left expired rows")
	}
}
